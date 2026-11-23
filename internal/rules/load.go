package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// Load reads every pack under dir/packs (recursively) and builds the engine.
// A directory without packs is an error: an engine with nothing to decide
// would turn every document into an exception without saying why.
func Load(dir string) (*Engine, error) {
	root := filepath.Join(dir, "packs")
	var packs []Pack
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !isYAML(path) {
			return nil
		}
		pack, err := ReadPack(path)
		if err != nil {
			return err
		}
		packs = append(packs, pack)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("load rule packs from %s: %w", root, err)
	}
	if len(packs) == 0 {
		return nil, fmt.Errorf("no rule packs under %s", root)
	}
	return New(packs)
}

// ReadPack parses one pack file and records its checksum.
func ReadPack(path string) (Pack, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator's rules directory (RULES_DIR), read only
	if err != nil {
		return Pack{}, err
	}
	var pack Pack
	if err := yaml.UnmarshalWithOptions(raw, &pack, yaml.Strict()); err != nil {
		return Pack{}, fmt.Errorf("%s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	pack.Checksum = hex.EncodeToString(sum[:])
	pack.Path = path
	return pack, nil
}

// FindDir resolves the rules directory: RULES_DIR, else ./rules or the
// repository root's rules/ relative to services/core (go run), else /rules
// (the container image).
func FindDir(configured string) (string, error) {
	candidates := []string{configured, "rules", filepath.Join("..", "..", "rules"), "/rules"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if info, err := os.Stat(filepath.Join(c, "packs")); err == nil && info.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("no rules directory found (set RULES_DIR)")
}

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}
