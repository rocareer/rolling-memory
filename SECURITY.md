# Security

**[English](SECURITY.md) · [简体中文](docs/zh-CN/SECURITY.md)**

## Supported versions

Security fixes are applied to the latest public release. Older releases are supported on a best-effort basis; please reproduce against the latest release before reporting.

## Reporting

Please report suspected vulnerabilities privately to the Chief Architect and maintainers (**Albert**, `support@psychologylake.org.cn`, Rocareer & Rolling-Memory Organization) rather than opening a public issue with exploit details. Once the public GitHub repository is live, GitHub Private Vulnerability Reporting is the preferred channel; email remains the fallback. Include the version, a minimal reproduction, and whether sensitive data or local-mode egress is involved. We aim to acknowledge reports on a best-effort basis and will coordinate disclosure after a fix is available.

## Security-relevant invariants

- Default `local` mode (no explicit `ROLLING_MEMORY_BRAIN_URL`) never builds an HTTP cloud transport and never sends a cloud request. An explicit `ROLLING_MEMORY_BRAIN_URL` is an intentional cloud override and should be treated as such.
- Protect sensitive data in local storage, backups, and any data shared with external services.
- API keys are read from environment variables or `0600` files; they are not stored in the database or config.
- The SQLite database is local-only and unencrypted; use full-disk encryption or an encrypted volume if the
  device is shared.
