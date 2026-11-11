# Security policy

## Supported versions

Only the latest release receives security fixes. Update to it before reporting.

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub: **Security → Report a vulnerability** on this repository ([direct link](https://github.com/toriath/hoard/security/advisories/new)). Include:

- the version (Settings → About) and how hoard is deployed
- what an attacker can do, and what they need (network access, an account, a crafted file)
- steps or a proof of concept

You can expect a first answer within a week. This is a spare-time project, so fixes take as long as they take; you will be told what is planned, and credited in the release notes unless you prefer otherwise.

## Scope

hoard is built for a private network: a home LAN or a tailnet, optionally behind a reverse proxy. All signed-in users are equally trusted by design; there are no roles. Reports that assume a different model, such as "one user can edit another user's assets", describe intended behaviour.

Especially welcome are reports about:

- anything reachable without signing in
- cross-site attacks against a signed-in user's browser
- uploaded files or asset metadata that execute code, escape the data directory or break out of an archive
- signed upload and download links being usable beyond their asset or lifetime
- secrets (passwords, tokens, the AI provider key) leaking through the API, logs or MCP
