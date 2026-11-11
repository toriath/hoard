# hoard

Self-hosted game-asset database: Go server (SQLite, REST, MCP) with an embedded React UI. README.md has the user-facing picture, CONTRIBUTING.md the contributor workflow. This file is the working context for AI coding assistants and is just as valid for people.

## Commands

- `make build`: web UI, then the single `./hoard` binary with the UI embedded
- `make test`: `go vet`, `go test -race ./...`, `npm --prefix web test` (vitest) and `scripts/next-version_test.sh`
- `npm --prefix web run typecheck`: TypeScript check; `npm --prefix web run build` also bundles
- `make image`: container image; `make notices`: third-party licence notices

`go build` needs `web/dist` to exist; `web/dist/.gitkeep` is committed for that and the web build recreates it.

## Layout

- `internal/hoard`: the core Service. All business rules live here; REST and MCP are thin wrappers. Add a capability here first, then expose it in `internal/server` and `internal/mcpserver`.
- `internal/server`: HTTP routing, sessions, file delivery. `server.go` holds middleware and auth, `assets.go` the handlers.
- `internal/mcpserver`: `mcpserver.go` holds the tools; tool descriptions are the product for agents, so keep them accurate when behaviour changes. `gallery.go` and `gallery.html` are the interactive gallery behind `show_assets` (an MCP App): one hand-written HTML file without a build step, embedded into the binary and speaking JSON-RPC over `postMessage` with the client.
- `internal/ai`: the metadata agent. `providers.go` talks to the Claude API (official Go SDK) and OpenAI-compatible servers (LM Studio).
- `web/src`: `ui/` is the design system (use these primitives, do not restyle ad hoc), `views/` the pages, `preview/` and `lib/thumbs.ts` the media previews (`preview/SpritePreview.tsx` plays sprite sheets from the `frame_width`/`frame_height`/`frames`/`columns`/`fps`/`loop`/`animations` attributes, parsed by `lib/sprite.ts`). The bundle is emitted to `dist/static`, because `/assets/<id>` is a page of the app.
- `scripts/`: `next-version.sh` (version from Conventional Commits) with its tests, `build-release.sh`, `third-party-notices.sh`.

## Release flow

- Work on a branch and open a pull request; never commit to `main` directly. PRs are squash-merged and **the PR title becomes the commit on `main`**, so it must be a Conventional Commit (`pr-title.yml` checks it). The title decides the release: `feat` is a minor, `fix`/`perf`/`revert` a patch, `!` after the type a major (a minor while 0.x); `docs`, `ci`, `chore`, `refactor`, `test`, `build` release nothing. Pick the type by what users of hoard get, not by what the diff touches.
- This relies on two repository settings: squash commit title = pull request title, squash commit message = blank. The command to set them is in the README under "Repository settings".
- A push to `main` runs `ci.yml`: Test, Version, Container image, and on a release also Release binaries and Release. Every push publishes the image as `edge` and `sha-<short>`. On a release, the Release job adds `X.Y.Z`, `X.Y` and `latest` to that same image digest and creates the `vX.Y.Z` tag and GitHub release, back to back and only after everything else succeeded. It runs no code from the repository; build jobs have read-only tokens.
- Never create version tags by hand. To force a version, run the CI workflow on `main` with `release_as` (it must be higher than the last release).
- **Merging a `feat` or `fix` PR is a release**: anyone following the `latest` image tag gets it. There is no staging.
- `security.yml` (govulncheck, npm audit, Trivy for secrets, Dockerfile and image) runs on PRs, on `main` and weekly, when it also rescans the released `latest` image. Its checks are deliberately not merge requirements: a newly published CVE should not block an unrelated PR.
- Actions are pinned to commit SHAs and base images to digests; Dependabot updates them. The Trivy image digest in `security.yml` is bumped by hand.
- The rulesets in `.github/rulesets/` protect `main` and release tags. If a required check's job name changes (`Test`, `Container image`, `PR title`), change it there too.
- buildx drops the Dockerfile `HEALTHCHECK` from the OCI image, so the compose files define the healthcheck themselves. Keep both.

## Security invariants

These came out of a security review. Do not weaken them without a reason written down in the PR.

- Paths on disk are only ever built from validated asset ids and file names read back from the index. `SanitizeFilename` also removes quote, backtick and dollar characters, because file names end up inside single-quoted shell commands that MCP tools hand to agents.
- Nothing with a side effect is a GET. Every state-changing route is covered by `http.CrossOriginProtection`.
- Account, token and AI-provider management require a browser session (`requireSession`); API tokens get `403 session_required`. A leaked token must not be able to make itself permanent.
- The AI provider key is only ever sent to the endpoint it was saved for: it is dropped when provider or base URL change, a key from the environment is only used with the endpoint the environment names, and it is never returned by the API.
- Sign-in attempts are counted before the password is checked (`limiter.take`), so parallel attempts cannot outrun the throttle. Credential request bodies are capped at 8 KiB and all JSON bodies have a read deadline.
- Signed links are bound to one asset and one file name, and uploads to trashed assets are refused.
- Images are decoded for thumbnails only within `maxDecodedBytes`, one at a time.
- The `show_assets` gallery runs inside the client's conversation. It builds its DOM from text nodes only, loads nothing over the network (so its resource declares no CSP domains), accepts only allow-listed image and audio types for `data:` URLs, and sends nothing but validated asset ids back into the conversation: an asset name must never become part of a user message. `TestGalleryViewIsSelfContained` guards this.
- User files are served with a fixed content type, `nosniff` and a sandboxing CSP. The app's own CSP allows only its origin plus `data:`/`blob:` for images and fetches (needed by the 3D loaders).
- `hoard.db` and its backups are mode 0600: they hold password hashes, the link-signing key and possibly the AI key.

## Verified and not

Covered by tests or checked in a real deployment: the web UI, MCP from a real client including uploads over signed links, the container on TrueNAS SCALE, access through a reverse proxy.

Never exercised against the real thing: the Claude API provider and LM Studio (both only against mock servers in `internal/ai/agent_test.go`), the Tailscale sidecar compose file, the `thv run` ToolHive command (flags taken from `thv run --help`, v0.53), and the `show_assets` gallery in a real client (only against the Go SDK client in `internal/server/mcp_client_test.go` and a hand-written mock host in a browser; whether Claude Desktop, its Code tab or claude.ai render it is unknown). Also checked only in jsdom (component tests), never in a real browser or on a phone: the animated sprite-sheet preview, the "More" menu of the phone navigation, the keyboard-operable waveform and the skip link. The index rebuild from sidecars is tested with a generated library, not with a real deployment. Do not describe these as working.

## Known gaps

- No password reset. An account is only created through first-run setup, `HOARD_ADMIN_*` (when no user exists) or another signed-in user. Until the first account exists, anyone who can reach the port can create it.
- Tag rename/merge exists in the core, REST and MCP but has no screen in the web UI.
- Thumbnails for audio, video, fonts, 3D models and image formats the server cannot decode are rendered by the browser (`web/src/lib/thumbs.ts`). Assets added only over MCP have none until the library is opened once, and the AI agent has no image for them before that.
- Links handed to agents are built from the request's `Host` (`Server.BaseURL`), which is not validated. A proxy that rewrites `Host` produces wrong links; `HOARD_PUBLIC_URL` overrides it globally.
- A rebuilt index (database lost, sidecars intact) has no users, tokens, settings, notes, requests, activity or project briefs; until the first account exists again anyone who can reach the port can create it.
- Sign-in throttling keys on the peer address, so all clients behind one reverse proxy share a bucket, unless `HOARD_TRUSTED_PROXIES` lists the proxy: then it keys on the right-most `X-Forwarded-For` address that is not a trusted proxy (`Server.clientIP`). `X-Forwarded-For` from any other peer is ignored. Only the throttle uses it; `Server.BaseURL` still uses `Host`.
- Revoking an API token does not revoke signed links it was already given (two hours at most). The link-signing key cannot be rotated without editing the database.
- Uploading a folder flattens it: relative paths are not kept, so files with the same name in one asset replace each other.
- The `show_assets` gallery plays only audio files up to 2 MiB and shows thumbnails only; video, 3D and larger audio need the web UI. It is stateless: reopening a conversation starts with nothing ticked.
- The image job has registry write permission also on pull requests from branches of this repository (forks and Dependabot get a read-only token).

## Conventions

- Schema changes: append a migration to `migrations` in `internal/hoard/db.go`; never edit an applied one.
- Mutations of one asset are serialised by `lockAssets` (a striped mutex set, taken before `syncSidecar`'s own `assetLock`; never the other way round, never while streaming an upload). New code that reads an asset and then writes it back must take it.
- Attributes the browser measures while rendering a preview (duration, sample rate, channels, size, triangles) go through `PUT /assets/{id}/technical` (`SetTechnicalAttrs`): an allow-list of numeric keys, no `updated_at` bump, no activity entry. Do not send them through `PATCH`.
- Every asset mutation must keep three things in sync: the DB row, the FTS row (`reindex`) and the `asset.json` sidecar (`syncSidecar`).
- New UI colours, sizes and controls go into `web/src/styles/tokens.css` / `components.css` first and show up on the `/design` page.
- Dialogs, menus and form fields come from `web/src/ui`; they handle focus, Escape and labelling. Do not wrap several controls in one `<label>`.
- Frontend tests (vitest + jsdom + Testing Library, `*.test.ts(x)` beside the code, setup in `web/src/test-setup.ts`) cover `lib/`, `views/filters.ts` and the `ui/` primitives (Dialog focus trap, Menu keyboard, Field labelling, TagInput). jsdom has no layout, so tests of components that measure must stub it. There are no tests of whole views or of the media previews.
- Tests exercise behaviour through the Service and real HTTP (`httptest`), including an MCP client from the official SDK. A security fix comes with a test that fails without it. Run `make test` before finishing.
- A new way to connect a client (like the Claude Code, Claude Desktop and ToolHive snippets) goes into both the Connect tab in `web/src/views/Settings.tsx` and the README's "Connect Claude" section.
- User-facing docs live in `README.md`, `docs/api.md` (REST reference) and `docs/truenas.md`. Update them in the same change as the behaviour.
- Asset names, descriptions, tags, prompts and file contents are untrusted input everywhere: in the browser, in shell commands, and in what MCP tools return to agents.
