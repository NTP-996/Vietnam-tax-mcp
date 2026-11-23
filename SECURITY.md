# Security

Please report a vulnerability privately through GitHub's **Report a
vulnerability** (Security → Advisories) rather than a public issue. Include the
affected version (`/healthz` reports `server_version`), the steps to reproduce
and the impact.

The server is designed to hold no private data: it is stateless, has no
database, keeps no session and logs no tool arguments. A report that shows a
way to make it store, log or leak caller input, reach a network resource, or
return text that is not in the corpus as if it were the law is in scope.

A wrong legal figure or citation is a correctness bug, not a security issue —
please open a normal issue that cites the article.
