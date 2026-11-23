#!/usr/bin/env python3
"""Build the legal corpus from original texts on the official portals.

The manifest ``legal/manifest.yaml`` lists every legal document the corpus
holds: its number, dates, the official URL of the original and the sha256 of
the bytes stored under ``legal/originals/``. This script

* **fetch** (the default): downloads each original into a fresh temporary
  directory, checks its sha256 against the manifest, stores it under
  ``legal/originals/`` and then builds the corpus;
* **--offline**: builds the corpus from the stored originals only;
* **--check**: validates manifest, originals, sha256, corpus frontmatter,
  dates and ``legal/corpus/INDEX.md`` without the network or pdftotext.

The build extracts the text (``pdftotext`` for PDF; the standard library's
HTML parser for the vbpl.vn document payload), normalises it to Unicode NFC,
removes the Official Gazette page furniture and splits it by ``Điều <n>``
into ``legal/corpus/<doc>/dieu-<n>.md``; text after the signature that
starts with ``Phụ lục`` goes to ``phu-luc.md``. Article text is kept as
written: khoản and điểm markers are not renumbered or reformatted, only the
PDF line wrapping is undone.

Downloads are untrusted data. They land in their own new directory, are only
hashed, copied and parsed, and nothing in them is executed. Run the script
with ``python3 -I``.

Every corpus file starts ``verified: false``. Only the reviewing accountant flips
a file to ``verified: true`` with ``verified_by``; a rebuild keeps that sign-
off while the article text is unchanged and drops it, with a warning, when
the text changes.

Usage:
    python3 -I scripts/legal_fetch.py [--doc ID ...]
    python3 -I scripts/legal_fetch.py --offline [--doc ID ...]
    python3 -I scripts/legal_fetch.py --check
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime
import hashlib
import html
import html.parser
import json
import re
import shutil
import subprocess
import sys
import tempfile
import unicodedata
import urllib.parse
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
LEGAL = REPO / "legal"
MANIFEST = LEGAL / "manifest.yaml"
ORIGINALS = LEGAL / "originals"
CORPUS = LEGAL / "corpus"
INDEX = CORPUS / "INDEX.md"

OFFICIAL_HOSTS = (
    "vbpl.vn",
    "congbao.chinhphu.vn",
    "congbaocdn.chinhphu.vn",
    "vanban.chinhphu.vn",
    "datafiles.chinhphu.vn",
    "chinhphu.vn",
    "gdt.gov.vn",
    "mof.gov.vn",
    "quochoi.vn",
)
STATUSES = ("obtained", "not_found_official")
FORMATS = ("pdf", "vbpl-html")
DOC_FIELDS = (
    "id",
    "number",
    "title",
    "type",
    "signed",
    "effective_from",
    "effective_to",
    "status",
)
OBTAINED_FIELDS = ("format", "source_url", "sha256", "retrieved")
FRONTMATTER_FIELDS = (
    "doc",
    "number",
    "article",
    "title",
    "chapter",
    "effective_from",
    "effective_to",
    "source_url",
    "sha256",
    "verified",
    "verified_by",
)
USER_AGENT = "Mozilla/5.0 (compatible; legal-corpus-fetch/1.0)"
TIMEOUT_SECONDS = 300
ISO_DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
DOC_ID = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")

# Article headings: "Điều 12. Title" at the start of a line. Quoted articles
# inside an amending text start with a quotation mark and never match.
ARTICLE_HEADING = re.compile(r"^Điều\s+(\d+)\s*\.\s*(.*)$")
# Vietnamese article titles never end with a full stop: a heading that does
# ("Điều 3. Quyết định này có hiệu lực … kể từ ngày ký.") carries text.
UNTITLED_ARTICLE = re.compile(r"\.$")
CHAPTER_HEADING = re.compile(
    r"^(Chương|CHƯƠNG|Mục|MỤC)\s+([IVXLC]+|\d+)\s*(?:[.:]\s*(.*))?$"
)
# The signature block ends the articles; a later "Phụ lục" starts annexes.
END_OF_ARTICLES = re.compile(
    r"^(Nơi nhận\s*:|TM\.\s|KT\.\s|TL\.\s|Q\.\s|CHỦ TỊCH QUỐC HỘI$"
    r"|(Luật|Nghị quyết) này (đã )?được Quốc hội)"
)
ANNEX_HEADING = re.compile(r"^(PHỤ LỤC|Phụ lục)\b")
# Official Gazette page furniture and the portal's signature stamp.
# In raw text order the page number may share the header's line, and the
# portals' signature stamp may sit anywhere on the page.
GAZETTE_HEADER = re.compile(r"^(\d{1,4}\s+)?CÔNG BÁO\s*/\s*Số\b.*$")
PAGE_NUMBER = re.compile(r"^\d{1,4}$")
SIGNATURE_STAMP = re.compile(
    r"^(Ký bởi:.*|Người ký: CỔNG THÔNG TIN.*"
    r"|Email: thongtinchinhphu@chinhphu\.vn"
    r"|Cơ quan: (VĂN PHÒNG CHÍNH PHỦ|Văn phòng Chính phủ)"
    r"|(Ngày ký|Thời gian ký): \d.*)$"
)
# A wrapped PDF line starts a new paragraph when it opens with a khoản or
# điểm marker, a dash or a quotation, or when the previous line ended one.
PARAGRAPH_START = re.compile(
    r"^(\d+[a-z]?\.(\s|$)|\d+(\.\d+)+\.?\s|[a-zđ]\)\s|[a-zđ]\.\d+\)\s"
    r"|[-–—•+*]\s|[“\"‘]|\((\d+|[a-zđ])\)\s)"
)
# A wrapped line shorter than this share of the full line width ended its
# paragraph even without final punctuation (a khoản heading, for example).
SHORT_LINE_RATIO = 0.7

PARAGRAPH_END = re.compile(r"[.:;!?…”\"]$")


class LegalError(Exception):
    """A manifest, download or corpus problem the run cannot recover from."""


@dataclasses.dataclass(frozen=True)
class Part:
    """One downloadable file of a document (most documents have one)."""

    url: str
    sha256: str
    file: Path


@dataclasses.dataclass
class Article:
    """One article as split from a document, before it is written out."""

    number: int
    title: str
    chapter: str | None
    part: Part
    lines: list[str] = dataclasses.field(default_factory=list)


# --------------------------------------------------------------------------
# Manifest: a deliberately small YAML subset, read with the standard library.
# --------------------------------------------------------------------------


def parse_scalar(raw: str) -> object:
    """Parses one manifest or frontmatter value.

    Args:
        raw: The text after ``key:``, already stripped.

    Returns:
        None, a bool, an int, or a string. Double-quoted values are JSON
        strings; single-quoted ones are YAML's, with '' for a quote (the
        manifest is formatted by Prettier, which prefers single quotes).
    """
    if raw in ("", "null", "~"):
        return None
    if raw in ("true", "false"):
        return raw == "true"
    if raw.startswith('"'):
        return json.loads(raw)
    if raw.startswith("'") and raw.endswith("'") and len(raw) > 1:
        return raw[1:-1].replace("''", "'")
    if re.fullmatch(r"-?\d{1,9}", raw):
        return int(raw)
    return raw


def parse_manifest(text: str) -> list[dict[str, object]]:
    """Parses ``legal/manifest.yaml``.

    The accepted shape is ``documents:`` followed by a list of mappings whose
    values are scalars, plus an optional ``parts:`` list of scalar mappings.

    Args:
        text: The manifest file content.

    Returns:
        The documents, in manifest order.

    Raises:
        LegalError: The text is outside the supported subset.
    """
    docs: list[dict[str, object]] = []
    current: dict[str, object] | None = None
    parts: list[dict[str, object]] | None = None
    seen_root = False
    for number, line in enumerate(text.splitlines(), start=1):
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        indent = len(line) - len(line.lstrip(" "))
        body = line.strip()
        if indent == 0:
            if body != "documents:":
                raise LegalError(f"manifest line {number}: expected documents:")
            seen_root = True
            continue
        item = body.startswith("- ")
        if item:
            body = body[2:]
        key, sep, raw = body.partition(":")
        if not sep or not re.fullmatch(r"[a-z_0-9]+", key):
            raise LegalError(f"manifest line {number}: expected key: value")
        value = parse_scalar(raw.strip())
        if indent == 2 and item:
            current = {key: value}
            docs.append(current)
            parts = None
        elif indent == 4 and current is not None and not item:
            if key == "parts" and value is None:
                parts = []
                current["parts"] = parts
            else:
                current[key] = value
                parts = None
        elif indent == 6 and item and parts is not None:
            parts.append({key: value})
        elif indent == 8 and not item and parts:
            parts[-1][key] = value
        else:
            raise LegalError(f"manifest line {number}: unexpected indentation")
    if not seen_root:
        raise LegalError("manifest: no documents: key")
    return docs


def original_suffix(doc: dict[str, object]) -> str:
    """Returns the file extension of a document's stored original."""
    return ".html" if doc.get("format") == "vbpl-html" else ".pdf"


def doc_parts(doc: dict[str, object]) -> list[Part]:
    """Returns the downloadable parts of an obtained document.

    A single-file document is described by ``source_url`` and ``sha256``; a
    multi-part one lists every part under ``parts`` and repeats the first
    part's URL and hash at the top level.

    Args:
        doc: One manifest document.

    Returns:
        The parts in order, each with the path of its stored original.
    """
    doc_id = str(doc["id"])
    suffix = original_suffix(doc)
    raw_parts = doc.get("parts")
    if not raw_parts:
        return [
            Part(
                str(doc["source_url"]),
                str(doc["sha256"]),
                ORIGINALS / f"{doc_id}{suffix}",
            )
        ]
    if not isinstance(raw_parts, list):
        raise LegalError(f"manifest {doc_id}: parts must be a list")
    return [
        Part(
            str(part["source_url"]),
            str(part["sha256"]),
            ORIGINALS / f"{doc_id}.part{index}{suffix}",
        )
        for index, part in enumerate(raw_parts, start=1)
    ]


def validate_manifest(docs: list[dict[str, object]]) -> list[str]:
    """Checks the manifest's fields, identifiers, URLs and dates.

    Args:
        docs: The parsed manifest.

    Returns:
        One message per problem; empty when the manifest is valid.
    """
    errors: list[str] = []
    seen: set[str] = set()
    for doc in docs:
        doc_id = str(doc.get("id"))
        where = f"manifest {doc_id}"
        missing = [field for field in DOC_FIELDS if field not in doc]
        if missing:
            errors.append(f"{where}: missing {', '.join(missing)}")
            continue
        if not DOC_ID.match(doc_id) or doc_id in seen:
            errors.append(f"{where}: id is malformed or repeated")
        seen.add(doc_id)
        if doc["status"] not in STATUSES:
            errors.append(f"{where}: status must be one of {STATUSES}")
        for field in ("signed", "effective_from"):
            if not ISO_DATE.match(str(doc[field])):
                errors.append(f"{where}: {field} is not YYYY-MM-DD")
        until = doc["effective_to"]
        if until is not None and (
            not ISO_DATE.match(str(until))
            or str(until) < str(doc["effective_from"])
        ):
            errors.append(f"{where}: effective_to is invalid or too early")
        if doc["status"] == "not_found_official":
            if not doc.get("reason"):
                errors.append(f"{where}: not_found_official needs a reason")
            continue
        missing = [field for field in OBTAINED_FIELDS if not doc.get(field)]
        if missing:
            errors.append(f"{where}: missing {', '.join(missing)}")
            continue
        if doc["format"] not in FORMATS:
            errors.append(f"{where}: format must be one of {FORMATS}")
        if not ISO_DATE.match(str(doc["retrieved"])):
            errors.append(f"{where}: retrieved is not YYYY-MM-DD")
        parts = doc_parts(doc)
        if doc.get("parts") and (
            parts[0].url != doc["source_url"]
            or parts[0].sha256 != doc["sha256"]
        ):
            errors.append(f"{where}: source_url/sha256 must equal part 1")
        for part in parts:
            if not SHA256.match(part.sha256):
                errors.append(f"{where}: sha256 {part.sha256!r} is malformed")
            if not is_official(part.url):
                errors.append(f"{where}: {part.url} is not an official portal")
        page = doc.get("portal_page")
        if page and not is_official(str(page)):
            errors.append(f"{where}: portal_page is not an official portal")
    return errors


def is_official(url: str) -> bool:
    """Returns whether a URL is HTTPS on one of the official portals."""
    parsed = urllib.parse.urlsplit(url)
    host = parsed.hostname or ""
    return parsed.scheme == "https" and any(
        host == official or host.endswith("." + official)
        for official in OFFICIAL_HOSTS
    )


def load_manifest() -> list[dict[str, object]]:
    """Reads and validates the manifest.

    Returns:
        The documents.

    Raises:
        LegalError: The manifest is invalid.
    """
    docs = parse_manifest(MANIFEST.read_text(encoding="utf-8"))
    errors = validate_manifest(docs)
    if errors:
        raise LegalError("\n".join(errors))
    return docs


# --------------------------------------------------------------------------
# Download
# --------------------------------------------------------------------------


def sha256_of(data: bytes) -> str:
    """Returns the hex sha256 of some bytes."""
    return hashlib.sha256(data).hexdigest()


def download(url: str, scratch: Path) -> bytes:
    """Downloads one URL into a scratch directory and returns its bytes.

    Args:
        url: An official-portal HTTPS URL (validated by the manifest).
        scratch: A fresh, empty directory that holds nothing else.

    Returns:
        The response body.
    """
    if not is_official(url):
        raise LegalError(f"{url} is not HTTPS on an official portal")
    # Checked above: HTTPS on an official host, never file: or a custom scheme.
    request = urllib.request.Request(  # noqa: S310
        url, headers={"User-Agent": USER_AGENT}
    )
    with urllib.request.urlopen(  # noqa: S310
        request, timeout=TIMEOUT_SECONDS
    ) as response:
        data = response.read()
    (scratch / "download.bin").write_bytes(data)
    return data


def vbpl_payload(page: bytes) -> bytes:
    """Extracts the document payload from a vbpl.vn document page.

    vbpl.vn renders each document page with fresh nonces, so the page bytes
    change on every request; the document itself travels as one HTML string
    inside the page's Next.js data and is byte-stable. That string is what is
    stored and hashed.

    Args:
        page: The downloaded vbpl.vn page.

    Returns:
        The document's HTML exactly as the portal serves it, UTF-8.

    Raises:
        LegalError: The page does not carry a document payload.
    """
    text = page.decode("utf-8")
    pushes = re.findall(
        r"self\.__next_f\.push\((\[.*?\])\)</script>", text, re.S
    )
    data = "".join(
        item[1]
        for item in map(json.loads, pushes)
        if len(item) > 1 and isinstance(item[1], str)
    )
    ref = re.search(
        r'"documentContent":\{"id":"[^"]*","title":[^,]*,'
        r'"content":"\$([0-9a-f]+)"',
        data,
    )
    if ref is None:
        raise LegalError("vbpl.vn page carries no documentContent reference")
    row = re.search(r"(?:^|\n)" + ref.group(1) + r":T([0-9a-f]+),", data)
    if row is None:
        raise LegalError("vbpl.vn page carries no document payload")
    encoded = data.encode("utf-8")
    start = len(data[: row.end()].encode("utf-8"))
    return encoded[start : start + int(row.group(1), 16)]


def fetch(docs: list[dict[str, object]]) -> int:
    """Downloads every obtained document and checks it against the manifest.

    A file whose sha256 matches is stored under ``legal/originals/``. A
    mismatch is an error: the new download is kept in its scratch directory
    for review and nothing is overwritten.

    Args:
        docs: The manifest documents to fetch.

    Returns:
        The number of mismatches or failures.
    """
    failures = 0
    ORIGINALS.mkdir(parents=True, exist_ok=True)
    for doc in docs:
        if doc["status"] != "obtained":
            print(f"skip   {doc['id']}: {doc['status']}")
            continue
        for part in doc_parts(doc):
            scratch = Path(tempfile.mkdtemp(prefix="legal-fetch-"))
            try:
                data = download(part.url, scratch)
                if doc["format"] == "vbpl-html":
                    data = vbpl_payload(data)
            except (OSError, LegalError, ValueError) as error:
                print(f"FAIL   {part.file.name}: {error}")
                failures += 1
                continue
            digest = sha256_of(data)
            if digest != part.sha256:
                (scratch / f"new{part.file.suffix}").write_bytes(data)
                print(
                    f"FAIL   {part.file.name}: sha256 {digest} != manifest; "
                    f"upstream changed? new copy in {scratch}"
                )
                failures += 1
                continue
            part.file.write_bytes(data)
            shutil.rmtree(scratch)
            print(f"ok     {part.file.name} ({len(data)} bytes)")
    return failures


# --------------------------------------------------------------------------
# Text extraction
# --------------------------------------------------------------------------


def clean_line(line: str) -> str:
    """Normalises one line: NFC, no soft hyphens, single spaces."""
    line = unicodedata.normalize("NFC", line)
    line = line.replace("\u00ad", "").replace("\u00a0", " ")
    line = line.replace("\u200b", "").replace("\ufeff", "")
    return re.sub(r"\s+", " ", line).strip()


def pdf_lines(path: Path) -> list[str]:
    """Extracts the lines of a PDF with pdftotext, minus page furniture.

    Args:
        path: A stored original PDF.

    Returns:
        Clean, non-empty lines in reading order.

    Raises:
        LegalError: pdftotext is missing or fails.
    """
    tool = shutil.which("pdftotext")
    if tool is None:
        raise LegalError("pdftotext not found: install poppler-utils")
    # A fixed tool on a path we chose; the PDF is parsed, never executed.
    result = subprocess.run(  # noqa: S603
        [tool, "-raw", "-enc", "UTF-8", str(path), "-"],
        capture_output=True,
        check=False,
    )
    if result.returncode != 0:
        raise LegalError(f"pdftotext failed on {path.name}")
    lines: list[str] = []
    for page in result.stdout.decode("utf-8", "replace").split("\f"):
        page_lines = [clean_line(line) for line in page.splitlines()]
        page_lines = [line for line in page_lines if line]
        page_lines = [
            line
            for line in page_lines
            if not SIGNATURE_STAMP.match(line)
            and not GAZETTE_HEADER.match(line)
        ]
        page_lines = strip_furniture(page_lines)
        lines.extend(page_lines)
    return lines


def strip_furniture(lines: list[str]) -> list[str]:
    """Removes the gazette header and the page number around one page."""
    head = 0
    while head < min(3, len(lines)) and (
        GAZETTE_HEADER.match(lines[head]) or PAGE_NUMBER.match(lines[head])
    ):
        head += 1
    tail = len(lines)
    while (
        tail > head
        and len(lines) - tail < 3
        and (
            GAZETTE_HEADER.match(lines[tail - 1])
            or PAGE_NUMBER.match(lines[tail - 1])
        )
    ):
        tail -= 1
    return lines[head:tail]


class _BlockText(html.parser.HTMLParser):
    """Turns document HTML into one line per block element.

    A table row becomes one line with its cells separated by " | "; the
    paragraphs inside a cell stay in that cell.
    """

    BLOCKS = frozenset(
        {"p", "div", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6", "table"}
    )
    SKIPPED = frozenset({"script", "style", "head"})

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.lines: list[str] = []
        self._current: list[str] = []
        self._skip_depth = 0
        self._furniture_depth = 0
        self._div_depth = 0
        self._cell_depth = 0

    def _flush(self) -> None:
        # A table row whose outer cells are empty leaves bare separators.
        line = clean_line("".join(self._current)).strip("| ")
        if line:
            self.lines.append(line)
        self._current = []

    def handle_starttag(
        self, tag: str, attrs: list[tuple[str, str | None]]
    ) -> None:
        if tag in self.SKIPPED:
            self._skip_depth += 1
            return
        if tag == "div":
            self._div_depth += 1
            classes = dict(attrs).get("class") or ""
            if self._furniture_depth == 0 and re.search(
                r"docx-(header|footer)", classes
            ):
                self._furniture_depth = self._div_depth
        if tag in ("td", "th"):
            self._cell_depth += 1
            if self._current:
                self._current.append(" | ")
        elif tag in self.BLOCKS:
            if self._cell_depth and tag not in ("tr", "table"):
                self._current.append(" ")
            else:
                self._flush()
        elif tag == "br":
            self._current.append(" ")

    def handle_endtag(self, tag: str) -> None:
        if tag in self.SKIPPED:
            self._skip_depth = max(0, self._skip_depth - 1)
            return
        if tag in ("td", "th"):
            self._cell_depth = max(0, self._cell_depth - 1)
        elif tag in self.BLOCKS and not (
            self._cell_depth and tag not in ("tr", "table")
        ):
            self._flush()
        if tag == "div":
            if self._furniture_depth == self._div_depth:
                self._furniture_depth = 0
                self._current = []
            self._div_depth -= 1

    def handle_data(self, data: str) -> None:
        if self._skip_depth == 0 and self._furniture_depth == 0:
            self._current.append(data)


def html_lines(path: Path) -> list[str]:
    """Extracts one line per block element from a stored vbpl.vn payload."""
    parser = _BlockText()
    parser.feed(path.read_text(encoding="utf-8"))
    parser.close()
    parser.lines.append("")
    return [line for line in parser.lines if line]


def unescape_check(text: str) -> str:
    """Decodes any entity the HTML parser left, and normalises to NFC."""
    return unicodedata.normalize("NFC", html.unescape(text))


# --------------------------------------------------------------------------
# Splitting
# --------------------------------------------------------------------------


def is_upper_title(line: str) -> bool:
    """Returns whether a line is an all-uppercase heading title."""
    letters = [char for char in line if char.isalpha()]
    return bool(letters) and line == line.upper()


def division_label(divisions: dict[str, str], heading: list[str]) -> str:
    """Records a Chương or Mục heading and returns the article's location.

    Args:
        divisions: The current chapter and section, updated in place; a new
            chapter clears the section.
        heading: The heading line and its uppercase title lines.

    Returns:
        "Chương II TITLE", or "Chương II TITLE — Mục 1 TITLE" inside a section.
    """
    label = " ".join(heading)
    if heading[0].upper().startswith("CHƯƠNG"):
        divisions.clear()
        divisions["chapter"] = label
    else:
        divisions["section"] = label
    return " — ".join(
        divisions[key] for key in ("chapter", "section") if key in divisions
    )


def split_articles(
    tagged: list[tuple[str, Part]], wrapped: bool
) -> tuple[list[Article], list[str], Part | None]:
    """Splits a document's lines into articles and an annex.

    Article numbers must run 1, 2, 3…; a "Điều n." line out of sequence is
    body text (for example an article quoted by an amending document).

    Args:
        tagged: Every line with the part it came from.
        wrapped: True for PDF text, whose lines are wrapped and whose
            article titles may continue on the next line.

    Returns:
        The articles, the annex lines (from the first "Phụ lục" after the
        signature) and the part the annex starts in.
    """
    articles: list[Article] = []
    chapter: str | None = None
    divisions: dict[str, str] = {}
    pending_heading: list[str] | None = None
    ended = False
    annex: list[str] = []
    annex_part: Part | None = None
    title_open = False
    for line, part in tagged:
        if ended:
            if annex or ANNEX_HEADING.match(line):
                annex_part = annex_part or part
                annex.append(line)
            continue
        heading = ARTICLE_HEADING.match(line)
        if heading and int(heading.group(1)) == len(articles) + 1:
            articles.append(
                Article(int(heading.group(1)), heading.group(2), chapter, part)
            )
            title_open = wrapped and not PARAGRAPH_END.search(line)
            pending_heading = None
            continue
        if articles and END_OF_ARTICLES.match(line):
            ended = True
            continue
        division = CHAPTER_HEADING.match(line)
        if division and (division.group(3) is None or is_upper_title(line)):
            pending_heading = [line]
            title_open = False
            continue
        if pending_heading is not None and is_upper_title(line):
            pending_heading.append(line)
            chapter = division_label(divisions, pending_heading)
            continue
        if pending_heading is not None:
            chapter = division_label(divisions, pending_heading)
            pending_heading = None
        if not articles:
            continue
        article = articles[-1]
        if (
            title_open
            and not article.lines
            and not PARAGRAPH_START.match(line)
            and (line[:1].islower() or re.search(r"\S-$", article.title))
        ):
            article.title = join_wrapped(article.title, line)
            title_open = not PARAGRAPH_END.search(line)
            continue
        title_open = False
        article.lines.append(line)
    for article in articles:
        if UNTITLED_ARTICLE.search(article.title):
            # The article has no title; its text starts on the heading line.
            article.lines.insert(0, article.title)
            article.title = ""
    return articles, annex, annex_part


def full_width(lines: list[str]) -> int:
    """Returns the typical length of a full, wrapped PDF line."""
    lengths = sorted(len(line) for line in lines)
    return lengths[int(len(lengths) * 0.9)] if lengths else 0


def join_wrapped(head: str, tail: str) -> str:
    """Joins a wrapped line to its paragraph.

    Vietnamese never splits a syllable across lines, so a line ending in a
    hyphen glued to a word ("NĐ-" before "CP") is a real hyphen and is joined
    without a space; a spaced dash ("kinh tế -") keeps the space.

    Args:
        head: The paragraph so far.
        tail: The next wrapped line.

    Returns:
        The joined paragraph.
    """
    if re.search(r"\S-$", head):
        return head + tail
    return f"{head} {tail}"


def paragraphs(lines: list[str], wrapped: bool, width: int = 0) -> list[str]:
    """Rejoins wrapped PDF lines into paragraphs; HTML lines already are.

    Args:
        lines: The lines of one article or annex.
        wrapped: True for PDF text.
        width: The document's full line width (see ``full_width``).

    Returns:
        One string per paragraph.
    """
    if not wrapped:
        return list(lines)
    joined: list[str] = []
    previous = ""
    for line in lines:
        continues = (
            joined
            and not PARAGRAPH_START.match(line)
            and not PARAGRAPH_END.search(previous)
            and len(previous) >= width * SHORT_LINE_RATIO
        )
        if continues:
            joined[-1] = join_wrapped(joined[-1], line)
        else:
            joined.append(line)
        previous = line
    return joined


# --------------------------------------------------------------------------
# Corpus files
# --------------------------------------------------------------------------


def yaml_value(value: object) -> str:
    """Formats a frontmatter value: null, bool, int, date or JSON string."""
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    text = str(value)
    if ISO_DATE.match(text):
        return text
    return json.dumps(text, ensure_ascii=False)


def render(meta: dict[str, object], heading: str, body: list[str]) -> str:
    """Renders one corpus file: frontmatter, heading, paragraphs."""
    front = "\n".join(f"{key}: {yaml_value(meta[key])}" for key in meta)
    text = "\n\n".join(body)
    return f"---\n{front}\n---\n\n# {heading}\n\n{text}\n"


def read_corpus_file(path: Path) -> tuple[dict[str, object], str]:
    """Reads a corpus file's frontmatter and the text after it.

    Args:
        path: A ``dieu-<n>.md`` or ``phu-luc.md`` file.

    Returns:
        The frontmatter mapping and the body.

    Raises:
        LegalError: The file has no frontmatter.
    """
    text = path.read_text(encoding="utf-8")
    match = re.match(r"^---\n(.*?)\n---\n", text, re.S)
    if match is None:
        raise LegalError(f"{path}: no frontmatter")
    meta: dict[str, object] = {}
    for line in match.group(1).splitlines():
        key, _, raw = line.partition(":")
        meta[key.strip()] = parse_scalar(raw.strip())
    return meta, text[match.end() :]


def build_doc(doc: dict[str, object]) -> int:
    """Extracts and splits one document into ``legal/corpus/<id>/``.

    Args:
        doc: An obtained manifest document whose originals are stored.

    Returns:
        The number of articles written.

    Raises:
        LegalError: An original is missing, altered, or yields no article.
    """
    doc_id = str(doc["id"])
    parts = doc_parts(doc)
    tagged: list[tuple[str, Part]] = []
    for part in parts:
        if not part.file.exists():
            raise LegalError(f"{part.file} is missing: run without --offline")
        if sha256_of(part.file.read_bytes()) != part.sha256:
            raise LegalError(f"{part.file} does not match its sha256")
        lines = (
            html_lines(part.file)
            if doc["format"] == "vbpl-html"
            else pdf_lines(part.file)
        )
        tagged.extend((unescape_check(line), part) for line in lines)
    wrapped = doc["format"] == "pdf"
    width = full_width([line for line, _ in tagged])
    articles, annex, annex_part = split_articles(tagged, wrapped)
    if not articles:
        raise LegalError(f"{doc_id}: no article found")
    target = CORPUS / doc_id
    previous = previous_signoffs(target)
    if target.exists():
        shutil.rmtree(target)
    target.mkdir(parents=True)
    for article in articles:
        body = paragraphs(article.lines, wrapped, width)
        name = f"dieu-{article.number}.md"
        meta = corpus_meta(doc, article.number, article.title, article.chapter)
        meta["source_url"] = article.part.url
        meta["sha256"] = article.part.sha256
        heading = f"Điều {article.number}. {article.title}".rstrip()
        keep_signoff(meta, previous.get(name), body, name, doc_id)
        (target / name).write_text(
            render(meta, heading, body), encoding="utf-8"
        )
    if annex and annex_part is not None:
        body = paragraphs(annex, wrapped, width)
        meta = corpus_meta(doc, "phu-luc", "Phụ lục", None)
        meta["source_url"] = annex_part.url
        meta["sha256"] = annex_part.sha256
        keep_signoff(
            meta, previous.get("phu-luc.md"), body, "phu-luc.md", doc_id
        )
        (target / "phu-luc.md").write_text(
            render(meta, "Phụ lục", body), encoding="utf-8"
        )
    return len(articles)


def corpus_meta(
    doc: dict[str, object],
    article: object,
    title: str,
    chapter: str | None,
) -> dict[str, object]:
    """Returns the frontmatter of a corpus file, unverified."""
    return {
        "doc": doc["id"],
        "number": doc["number"],
        "article": article,
        "title": title.strip(),
        "chapter": chapter,
        "effective_from": doc["effective_from"],
        "effective_to": doc["effective_to"],
        "source_url": None,
        "sha256": None,
        "verified": False,
        "verified_by": None,
    }


def previous_signoffs(
    target: Path,
) -> dict[str, tuple[dict[str, object], list[str]]]:
    """Reads the sign-offs and bodies of a document's existing corpus files."""
    signoffs: dict[str, tuple[dict[str, object], list[str]]] = {}
    if not target.exists():
        return signoffs
    for path in target.glob("*.md"):
        meta, text = read_corpus_file(path)
        body = text.lstrip("\n").partition("\n\n")[2].rstrip("\n")
        signoffs[path.name] = (meta, body.split("\n\n") if body else [])
    return signoffs


def keep_signoff(
    meta: dict[str, object],
    previous: tuple[dict[str, object], list[str]] | None,
    body: list[str],
    name: str,
    doc_id: str,
) -> None:
    """Carries the reviewing accountant's sign-off over an unchanged rebuild."""
    if previous is None or previous[0].get("verified") is not True:
        return
    if previous[1] == body:
        meta["verified"] = True
        meta["verified_by"] = previous[0].get("verified_by")
    else:
        print(f"WARN   {doc_id}/{name}: text changed; sign-off dropped")


def write_index(docs: list[dict[str, object]]) -> None:
    """Writes ``legal/corpus/INDEX.md`` from the manifest and the corpus."""
    CORPUS.mkdir(parents=True, exist_ok=True)
    INDEX.write_text(index_text(docs), encoding="utf-8")


def index_text(docs: list[dict[str, object]]) -> str:
    """Renders the corpus index from the manifest and the corpus files."""
    rows = []
    for doc in docs:
        target = CORPUS / str(doc["id"])
        if doc["status"] == "obtained" and target.exists():
            count = len(list(target.glob("dieu-*.md")))
            verified = sum(
                1
                for path in target.glob("*.md")
                if read_corpus_file(path)[0].get("verified") is True
            )
            annex = "yes" if (target / "phu-luc.md").exists() else "no"
            link = f"[{doc['id']}]({doc['id']}/)"
        else:
            count, verified, annex, link = 0, 0, "—", str(doc["id"])
        until = doc["effective_to"] or "—"
        rows.append(
            f"| {link} | {doc['number']} | {doc['title']} | {doc['signed']} "
            f"| {doc['effective_from']} | {until} | {count} | {annex} "
            f"| {verified} | {doc['status']} |"
        )
    return (
        "# Legal corpus index\n\n"
        "Generated by `scripts/legal_fetch.py` from `legal/manifest.yaml`; "
        "do not edit by hand. Every article is `verified: false` until the "
        "reviewing accountant signs it off against the original. Dates are the "
        "document's own; an article whose hiệu lực differs is governed by "
        "the document's own effect article.\n\n"
        "| Doc | Number | Title | Signed | Effective from | Effective to "
        "| Articles | Annex | Verified | Status |\n"
        "|---|---|---|---|---|---|---|---|---|---|\n" + "\n".join(rows) + "\n"
    )


def build(
    docs: list[dict[str, object]], all_docs: list[dict[str, object]]
) -> int:
    """Builds the corpus of the given documents and rewrites the index.

    Args:
        docs: The documents to rebuild.
        all_docs: The whole manifest, for the index.

    Returns:
        The number of documents that failed.
    """
    failures = 0
    for doc in docs:
        if doc["status"] != "obtained":
            continue
        try:
            count = build_doc(doc)
        except LegalError as error:
            print(f"FAIL   {doc['id']}: {error}")
            failures += 1
            continue
        print(f"built  {doc['id']}: {count} articles")
    write_index(all_docs)
    return failures


# --------------------------------------------------------------------------
# Check
# --------------------------------------------------------------------------


def check(docs: list[dict[str, object]]) -> list[str]:
    """Validates originals, corpus frontmatter, dates and the index.

    Args:
        docs: The validated manifest.

    Returns:
        One message per problem; empty when everything agrees.
    """
    errors: list[str] = []
    known = {str(doc["id"]) for doc in docs}
    stored = {path.name for path in ORIGINALS.glob("*") if path.is_file()}
    expected: set[str] = {".gitattributes"}
    for doc in docs:
        doc_id = str(doc["id"])
        target = CORPUS / doc_id
        if doc["status"] != "obtained":
            if target.exists():
                errors.append(
                    f"{doc_id}: corpus exists but status is {doc['status']}"
                )
            continue
        parts = doc_parts(doc)
        for part in parts:
            expected.add(part.file.name)
            if not part.file.exists():
                errors.append(f"{doc_id}: original {part.file.name} is missing")
            elif sha256_of(part.file.read_bytes()) != part.sha256:
                errors.append(f"{doc_id}: {part.file.name} sha256 mismatch")
        errors.extend(check_corpus(doc, parts, target))
    for name in sorted(stored - expected):
        errors.append(f"originals/{name} is not in the manifest")
    for path in CORPUS.iterdir() if CORPUS.exists() else []:
        if path.is_dir() and path.name not in known:
            errors.append(f"corpus/{path.name} is not in the manifest")
    if not INDEX.exists() or INDEX.read_text(encoding="utf-8") != index_text(
        docs
    ):
        errors.append("corpus/INDEX.md is stale: rerun legal-fetch --offline")
    return errors


def check_corpus(
    doc: dict[str, object], parts: list[Part], target: Path
) -> list[str]:
    """Validates one document's corpus files against its manifest entry."""
    doc_id = str(doc["id"])
    if not target.is_dir():
        return [f"{doc_id}: corpus/{doc_id}/ is missing"]
    errors: list[str] = []
    urls = {part.url: part.sha256 for part in parts}
    numbers = []
    for path in sorted(target.iterdir()):
        name = path.name
        match = re.fullmatch(r"dieu-(\d+)\.md", name)
        if not match and name != "phu-luc.md":
            errors.append(f"{doc_id}/{name}: unexpected file")
            continue
        meta, body = read_corpus_file(path)
        where = f"{doc_id}/{name}"
        if tuple(meta) != FRONTMATTER_FIELDS:
            errors.append(f"{where}: frontmatter keys differ from the contract")
            continue
        article: object = int(match.group(1)) if match else "phu-luc"
        if match:
            numbers.append(int(match.group(1)))
        for key, want in (
            ("doc", doc_id),
            ("number", doc["number"]),
            ("article", article),
            ("effective_from", doc["effective_from"]),
            ("effective_to", doc["effective_to"]),
        ):
            if meta[key] != want:
                errors.append(f"{where}: {key} is {meta[key]!r}, want {want!r}")
        if urls.get(str(meta["source_url"])) != meta["sha256"]:
            errors.append(
                f"{where}: source_url/sha256 do not match an original"
            )
        if meta["verified"] is True and not meta["verified_by"]:
            errors.append(f"{where}: verified without verified_by")
        elif meta["verified"] not in (True, False):
            errors.append(f"{where}: verified must be true or false")
        if unicodedata.normalize("NFC", body) != body:
            errors.append(f"{where}: text is not NFC")
    if sorted(numbers) != list(range(1, len(numbers) + 1)):
        errors.append(f"{doc_id}: article numbers are not 1..{len(numbers)}")
    return errors


# --------------------------------------------------------------------------
# Command line
# --------------------------------------------------------------------------


def main(argv: list[str] | None = None) -> int:
    """Runs fetch + build, the offline build, or the check.

    Args:
        argv: Command-line arguments; defaults to ``sys.argv[1:]``.

    Returns:
        The process exit code.
    """
    parser = argparse.ArgumentParser(
        description=__doc__.split("\n\n", maxsplit=1)[0]
    )
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="validate only")
    mode.add_argument(
        "--offline", action="store_true", help="build from stored originals"
    )
    parser.add_argument(
        "--doc", action="append", default=[], help="limit to a document id"
    )
    args = parser.parse_args(argv)
    try:
        docs = load_manifest()
    except (LegalError, OSError, ValueError) as error:
        print(f"FAIL   manifest: {error}")
        return 1
    if args.check:
        errors = check(docs)
        for error in errors:
            print(f"FAIL   {error}")
        obtained = sum(1 for doc in docs if doc["status"] == "obtained")
        print(
            f"legal-check: {len(docs)} documents, {obtained} obtained, "
            f"{len(errors)} errors"
        )
        return 1 if errors else 0
    selected = [doc for doc in docs if not args.doc or doc["id"] in args.doc]
    unknown = set(args.doc) - {str(doc["id"]) for doc in docs}
    if unknown:
        print(f"FAIL   unknown document ids: {', '.join(sorted(unknown))}")
        return 1
    failures = 0 if args.offline else fetch(selected)
    failures += build(selected, docs)
    print(
        f"legal-fetch: {datetime.date.today().isoformat()}, {failures} failures"
    )
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
