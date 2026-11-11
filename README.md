# hoard

[![CI](https://github.com/toriath/hoard/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/toriath/hoard/actions/workflows/ci.yml)
[![Security](https://github.com/toriath/hoard/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/toriath/hoard/actions/workflows/security.yml)
[![Release](https://img.shields.io/github/v/release/toriath/hoard)](https://github.com/toriath/hoard/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

A self-hosted database for game assets: sprites, 3D models, textures, SFX, music, VFX, shaders, video, fonts and whatever else a game needs. One small container, one data directory, a web UI for people and an MCP server for Claude.

- **AI first.** Every feature is available to Claude Code and Claude Desktop over MCP with an API token: search, download into a project, store newly generated assets with their prompts, tag, group, track usage.
- **For humans too.** A fast web UI for desktop and phone with previews for every media kind, search, sorting, bulk actions and downloads.
- **Your data, readable.** Original files are stored untouched next to a JSON sidecar per asset. No lock-in, trivial backups.
- **Built-in AI agent** that names, describes and tags assets, using the Claude API or a local model in LM Studio.

## Contents

- [Run it](#run-it)
- [Install on TrueNAS SCALE](#install-on-truenas-scale)
- [Reach it over your tailnet](#reach-it-over-your-tailnet)
- [Connect Claude (MCP)](#connect-claude-mcp)
- [The built-in AI agent](#the-built-in-ai-agent)
- [What an asset holds](#what-an-asset-holds)
- [Where your data lives](#where-your-data-lives)
- [Configuration](#configuration)
- [Security](#security)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Run it

```bash
docker run -d --name hoard -p 8080:8080 -v hoard-data:/data ghcr.io/toriath/hoard:latest
```

Open <http://localhost:8080>, create the first account, and drop some files onto the page. For a permanent installation use [`deploy/compose.yaml`](deploy/compose.yaml), which also locks the container down.

Images are built by GitHub Actions and published to the repository's container registry (`ghcr.io/toriath/hoard`) for `linux/amd64` and `linux/arm64`:

| Tag | What it is |
| --- | --- |
| `latest` | the newest release |
| `0.1.0`, `0.1` | one exact release; the newest patch of a minor line |
| `edge` | the tip of `main`, released or not |

Each release also has a [GitHub release](https://github.com/toriath/hoard/releases) with notes and standalone binaries for Linux and macOS.

## Install on TrueNAS SCALE

For TrueNAS SCALE 24.10 ("Electric Eel") and newer, which run apps with Docker. The short version follows; [`docs/truenas.md`](docs/truenas.md) is the full guide with updates, backups and troubleshooting.

1. **Create a dataset** for the data, for example `tank/apps/hoard` (Datasets → Add Dataset, preset *Apps*). Under *Permissions*, make user `apps` (UID 568) the owner, or grant it full control.
2. **Install the app:** Apps → Discover Apps → ⋮ → **Install via YAML**. Name it `hoard` and paste [`deploy/compose.yaml`](deploy/compose.yaml). Adjust the dataset path and the time zone.
3. Open `http://<truenas>:8080` and create the first account straight away: until one exists, anyone who can reach the port can create it.

To update, pull the new image from the app's page in TrueNAS (or `docker compose pull && docker compose up -d`). The database migrates itself.

## Reach it over your tailnet

hoard builds every link from the address you used to reach it, so one instance serves the LAN and the tailnet at the same time without configuration.

- **TrueNAS is already on your tailnet** (for example through the TrueNAS Tailscale app with host networking): nothing to do. Use `http://<truenas-tailnet-name>:8080`.
- **You want HTTPS and a name of its own** (`https://hoard.<tailnet>.ts.net`): use [`deploy/compose.tailscale.yaml`](deploy/compose.tailscale.yaml) instead. It adds a Tailscale sidecar that terminates TLS with your tailnet certificate. Tailnet only; Funnel stays off.

Do not expose hoard to the public internet. It is built for a private network.

## Connect Claude (MCP)

The MCP endpoint is `http://<host>:8080/mcp` (Streamable HTTP). **Settings → Connect Claude** in the web UI creates a token and shows these snippets with your address and token already filled in.

**Claude Code**, once, for all projects:

```bash
claude mcp add --transport http --scope user hoard http://truenas.local:8080/mcp --header "Authorization: Bearer hoard_…"
```

**Per repository**, as `.mcp.json` (keeps the secret in your shell environment, not in git):

```json
{
  "mcpServers": {
    "hoard": {
      "type": "http",
      "url": "http://truenas.local:8080/mcp",
      "headers": { "Authorization": "Bearer ${HOARD_TOKEN}" }
    }
  }
}
```

**Claude Desktop**, in `claude_desktop_config.json` (bridged with `mcp-remote`, needs Node.js; `--allow-http` is only required for plain-HTTP addresses):

```json
{
  "mcpServers": {
    "hoard": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://truenas.local:8080/mcp", "--transport", "http-only", "--allow-http", "--header", "Authorization:${HOARD_AUTH}"],
      "env": { "HOARD_AUTH": "Bearer hoard_…" }
    }
  }
}
```

**ToolHive**, as a remote workload that it then offers to the AI clients you enabled there:

```bash
thv run http://truenas.local:8080/mcp --name hoard --transport streamable-http --remote-auth-bearer-token hoard_…
```

Then just ask:

> "Find a pixel-art sword in my hoard and add it to this game."
> "Store the sprites in `out/` in hoard. Tag them, record the prompt I used, and add them to the Emberfall project."
> "Which assets in my hoard have no license recorded?"
> "Show me footstep sounds for a stone dungeon and let me pick."

### Choosing assets in the conversation

`show_assets` is an [MCP App](https://modelcontextprotocol.io/extensions/apps/overview): in a client that renders interactive views, the assets appear as a gallery in the conversation, with thumbnails and playback for audio files up to 2 MiB. Tick what you want and press **Use selected**; the agent receives the ids of your choice and carries on.

The gallery loads nothing from the network. Previews reach it through the client, so it also works when only your own machine can reach hoard. Clients without interactive views get the same assets as a list with links to the web UI.

This has only been tested against a simulated client so far, not in a real one.

### How agents move files

Metadata travels through tool calls. Binary files do not pass through the model: tools hand out short-lived signed links as ready-to-run `curl` commands, so a 200 MB model costs no context and the commands contain no API token. Each link is valid for two hours and for one named file of one asset. The commands are written for a POSIX shell (bash, zsh, sh).

| Tool | Purpose |
| --- | --- |
| `hoard_overview` | Counts, valid types, license suggestions, collections, projects, top tags |
| `search_assets` | Full-text search with filters (type, tags, collection, project, license, dominant colours) and paging |
| `get_asset` | Full record plus download commands |
| `view_asset` | Look at the preview image, or read a text file such as a shader |
| `show_assets` | Show assets to you as an interactive gallery to look, listen and pick (see above) |
| `create_asset` | Create with metadata; returns upload commands (or takes small text files inline) |
| `get_upload_commands` / `get_download_commands` | Signed `curl` commands for files, single or zipped |
| `update_asset` / `bulk_update_assets` | Edit metadata, tags, collections and project usage |
| `delete_assets` / `restore_assets` | Trash (recoverable) or permanent delete |
| `list_tags` / `rename_tag` | Tag vocabulary maintenance |
| `list_collections` / `save_collection` / `delete_collection` | Packs, styles and other groups |
| `list_projects` / `save_project` / `delete_project` | Projects that assets are used in |
| `analyze_assets` | Run the built-in AI agent |
| `find_similar` | Duplicates and look-alikes of one asset, with scored reasons ("identical file", "visually near-identical (distance 3)", "shares 4 tags") |
| `find_duplicates` | Groups of identical files and near-identical pictures across the whole library |
| `get_import_command` | A signed `curl -T pack.zip` command that imports a zip archive as a pack (see below) |

`asset_media` also exists; the gallery uses it to play audio and it is of no use to an agent.

The same token works for the REST API under `/api/v1`; see [`docs/api.md`](docs/api.md).

### Duplicates, similar assets and colours

For every picture the server computes a 64-bit perceptual hash (a difference hash of the decoded image, no second decode) and up to five dominant colours. Pictures stored before this existed are indexed in the background after the server starts.

- **Upload warnings.** The result of an upload lists `duplicate_of` (identical bytes) and `similar_to` (near-identical pictures: the same image re-encoded, rescaled or lightly touched, at most 5 of 64 hash bits apart).
- **Similar assets.** The asset drawer lists look-alikes with the reason for each; `find_similar` and `GET /assets/{id}/similar` return the same. Non-pictures are matched by identical files and by the same type with overlapping tags.
- **Duplicates view.** *Find duplicates…* in the Library's view menu (or `find_duplicates`, `GET /duplicates`) lists groups of identical files and of near-identical pictures, where you can open them and move surplus ones to the trash.
- **Colour search.** The Library's filter dialog has colour swatches; `search_assets` takes `colors` and the REST list takes `color=ff3c3c`. A colour is matched to the nearest of 53 palette colours (12 hues in four variants plus greys) and an asset matches when that colour makes up at least 10 % of its opaque pixels. Several colours must all be present.

The hash and colours describe the primary file of an asset when it is an image the server can decode (PNG, JPEG, GIF, WebP, BMP, TIFF). SVG and AVIF pictures have neither.

### Importing a pack

Drop a single `.zip` on the web UI and choose **Import as pack**, or let an agent call `get_import_command` for a `curl -T pack.zip` command (`POST /import` does the same for scripts). hoard unpacks the archive on the server and creates a collection of kind pack, named after the zip or `collection_name`, with one of three layouts: `per_file` (one asset per file, the default), `per_folder` (one asset per top-level folder; files in sub-folders keep their path as `sub_folder_file.png`; root files stay separate) or `single`. Tags and a project can be given for all assets.

The licence is read from `LICENSE*`, `COPYING*`, `README*`, `CREDITS*` and `.txt`/`.md` files in the archive root (CC0, CC-BY, CC-BY-SA, CC-BY-NC, OFL, MIT, Apache-2.0 and public domain, by their wording or by a creativecommons.org link), together with a line such as "Created by …" as attribution, and applied to every asset unless you pass a licence. Detection is by keywords, not legal advice: read the summary and correct it. The summary also lists skipped and failed files.

The archive is treated as hostile: at most 10 000 entries, at most the upload limit unpacked, no entry that compresses more than 200:1 once it is larger than 4 MiB (a zip bomb), no symlinks, encrypted or empty entries; `__MACOSX`, dotfiles and folders are skipped; entry paths never reach the disk, only a sanitised file name does.

## The built-in AI agent

Configure it under **Settings → AI agent**. It looks at an asset's preview image, its files and its existing metadata and proposes a name, a description, tags and a type. By default it only fills gaps: tags are merged, a description you wrote stays. Use it per asset (*AI fill*, or *Suggest with AI* while editing), on a selection (*AI tag*), or automatically on upload.

- **Claude API.** Paste an API key. The default model is `claude-opus-5-5`; `claude-haiku-5-5` is far cheaper and usually good enough for tagging.
- **Local model.** Any OpenAI-compatible server. For LM Studio: Developer → Local Server → start, enable *Serve on Local Network*, and enter `http://<that-machine>:1234/v1`. Inside the container `localhost` is the container itself, so use the LAN or tailnet address. Choose a vision-capable model if previews should be analysed; text-only models still work from file names and metadata.

Previews are made where they can be: the server renders thumbnails for images it can decode, a waveform for WAV, MP3, Ogg Vorbis and FLAC (the first ten minutes are decoded, and duration, sample rate, channels, bit depth and peak level are recorded as attributes when the asset does not have them yet) and a specimen ("Aa Bb Cc", digits, the font's name) for TrueType and OpenType fonts, all without ffmpeg. It only makes a thumbnail when the asset has none; a thumbnail from the browser or from you is never replaced. Video, 3D models, WOFF/WOFF2 fonts, Opus and other audio formats still get theirs from the browser the first time they appear in the web UI, and the agent then sees that image; assets of those kinds added purely over MCP have no preview until someone opens the library once.

## What an asset holds

| Field | Notes |
| --- | --- |
| ID | Stable, short, URL-safe: `ast_7k2m9q4x1c8v` |
| Name, description | Free text |
| Type | sprite, tileset, ui, texture, model, animation, vfx, shader, sfx, music, voice, video, font, other. Guessed from the first file, always editable |
| Tags | Lowercase kebab-case, with autocomplete in the web UI; rename and merge over MCP and REST |
| Prompt | The prompt the asset was generated with |
| Source, source URL | Generator, marketplace, artist |
| License, license URL, attribution | SPDX ids suggested, free text allowed |
| Files | One or many (a model with its textures, a sheet with its atlas). One is primary and drives the preview |
| Attributes | Free-form technical data. Dimensions, duration, triangle count are filled in automatically |
| Collections | Logical groups: pack, style, project set, other |
| Projects | Where the asset is used, with an optional usage note |

## Where your data lives

Everything is below the `/data` volume:

```text
/data
├── hoard.db                 SQLite index: metadata, tags, search, users
├── assets/<asset-id>/
│   ├── asset.json           all metadata of the asset, rewritten on every change
│   ├── thumb.png|jpg        preview thumbnail
│   └── files/<filename>     your original files, byte for byte
├── backups/hoard-<date>.db  daily consistent snapshots of the index
└── tmp/                     uploads in flight
```

A ZFS snapshot of the dataset is a complete backup. The sidecars are a safety net and a way to use the library without hoard: if the index were lost, every asset would still be there with its metadata, but hoard does not rebuild the index from them, and accounts, tokens and settings live only in the index.

## Configuration

All optional. Set as environment variables on the container.

| Variable | Default | Meaning |
| --- | --- | --- |
| `HOARD_ADDR` | `:8080` | Listen address |
| `HOARD_DATA_DIR` | `/data` | Data root |
| `HOARD_MAX_UPLOAD_MB` | `4096` | Per-file upload limit |
| `HOARD_TRASH_DAYS` | `30` | Empty the trash after this many days (`0` keeps it forever) |
| `HOARD_BACKUP_KEEP` | `7` | Daily index snapshots to keep (`0` disables) |
| `HOARD_PUBLIC_URL` | – | Force one base URL in links handed to agents. Normally not needed |
| `HOARD_ADMIN_USER`, `HOARD_ADMIN_PASSWORD` | – | Create the first account at startup instead of in the browser |
| `HOARD_AI_PROVIDER`, `HOARD_AI_BASE_URL`, `HOARD_AI_MODEL`, `HOARD_AI_API_KEY` | – | Defaults for the AI agent (`anthropic` or `openai`). Settings saved in the UI win |
| `HOARD_LOG_LEVEL` | `info` | `debug` for more detail |

## Security

hoard is built for a private network: a LAN or a tailnet, optionally behind a reverse proxy. Do not expose it to the public internet. To report a vulnerability, see [SECURITY.md](SECURITY.md).

- **Accounts:** passwords are hashed with Argon2id; sessions are HttpOnly, SameSite cookies stored hashed; sign-in attempts are throttled per account and per client address. Every user has full access (there are no roles). Behind a reverse proxy all clients share one address, so they also share the per-address limit.
- **API tokens** are shown once and stored only as SHA-256 hashes. They can do everything with the library, but they cannot manage accounts, tokens or the AI provider: that needs a signed-in browser session, so a leaked token cannot make itself permanent. Revoke tokens individually in the UI; links a token has already been handed stay valid until they expire (two hours at most). MCP accepts tokens only, never browser sessions.
- **Cross-site requests** that would change data are rejected, nothing with a side effect is a GET, and the UI ships a strict Content-Security-Policy.
- **Uploaded files are inert in the browser:** served with a fixed content type, `nosniff` and a sandboxing CSP, so an uploaded HTML or SVG file cannot run script in your session. File names are sanitised, including characters that could break out of the shell commands handed to agents, and files are only ever read through the index. Images are only decoded for thumbnails within a memory budget.
- **Asset metadata is untrusted text.** Names, descriptions and prompts written by one agent are read by another. The MCP server tells clients to treat them as data, but prompt injection through stored content cannot be ruled out by hoard; review what agents do with assets from sources you do not trust.
- **Container:** static binary on a distroless base, non-root (UID 568), all capabilities dropped and, with the provided compose file, a read-only root filesystem. It writes only to `/data`.
- **Secrets at rest:** the index (`hoard.db`) holds password hashes, the link-signing key and, if you save one in the UI, the AI provider key. It is readable by its owner only; keep its backups just as private. The key is never sent back to the browser and only ever sent to the provider address it was saved for.
- Plain HTTP on a trusted LAN is fine. Anywhere else, put TLS in front (the tailnet variant does).
- **Scanning:** the Security workflow checks every change, and `main` and the latest released image weekly: govulncheck for vulnerabilities in the Go code hoard actually calls, npm audit for the shipped web dependencies, Trivy for the built image, for secrets in the working tree and for Dockerfile mistakes. High and critical findings fail the run. All GitHub Actions are pinned to commit hashes and base images to digests.

## Development

Requirements: Go 1.27+, Node 24+.

```bash
make build   # builds the web UI and a single ./hoard binary with it embedded
```

```bash
make run     # runs it on :8080 with data in ./data
```

```bash
make test    # go vet, go test -race, and the release version tests
```

For UI work, run the server and, in a second terminal, `npm --prefix web run dev` (Vite proxies `/api` to `:8080`).

```text
cmd/hoard            entry point, housekeeping, healthcheck
internal/hoard       core: data model, SQLite index, files on disk, auth
internal/server      REST API, sessions, file delivery, embedded web UI
internal/mcpserver   MCP tools (thin wrappers over the core)
internal/ai          metadata agent: Claude API and OpenAI-compatible providers
web/                 React + TypeScript single-page app
web/src/styles       design system: tokens.css, components.css; app.css for layouts
deploy/              compose files for TrueNAS / Docker
scripts/             release tooling: next version from commits, release binaries, licence notices
```

### Commits, versions and releases

Changes reach `main` as squash-merged pull requests, and the PR title is the commit message. It must be a [Conventional Commit](https://www.conventionalcommits.org), which a check enforces, because the version is derived from it:

| PR title starts with | Effect on merge |
| --- | --- |
| `feat:` | minor release (0.1.0 → 0.2.0) |
| `fix:`, `perf:`, `revert:` | patch release (0.1.0 → 0.1.1) |
| `feat!:` or any `type!:` | major release; a minor one while the version is 0.x |
| `docs:`, `ci:`, `chore:`, `refactor:`, `test:`, `build:` | no release; the change is on `edge` only |

A scope is optional: `fix(mcp): reject empty ids`. The very first release is 0.1.0, whatever the commits say.

On a release the pipeline builds the binaries, gives the already published image its version tags and `latest`, and creates the `vX.Y.Z` tag and a GitHub release with generated notes. Nothing is tagged by hand. To force a specific version, for example `1.0.0`, run the CI workflow manually from `main` with *release_as*. The rules live in [`scripts/next-version.sh`](scripts/next-version.sh) and are tested by `scripts/next-version_test.sh`.

### Repository settings

The release flow depends on two repository settings, and `main` is protected by rulesets kept in [`.github/rulesets/`](.github/rulesets): pull requests only, squash merges, passing *Test*, *Container image* and *PR title* checks, no force pushes or deletion; release tags cannot be moved or deleted. To set up a fork or a fresh copy the same way:

```bash
gh api -X PATCH repos/OWNER/REPO -F allow_merge_commit=false -F allow_rebase_merge=false -F delete_branch_on_merge=true -f squash_merge_commit_title=PR_TITLE -f squash_merge_commit_message=BLANK
```

```bash
gh api repos/OWNER/REPO/rulesets --method POST --input .github/rulesets/main.json
```

```bash
gh api repos/OWNER/REPO/rulesets --method POST --input .github/rulesets/release-tags.json
```

GitHub enforces rulesets on public repositories and on paid plans.

### Design system

The UI is built on a small design system: tokens in [`web/src/styles/tokens.css`](web/src/styles/tokens.css), primitives in `components.css` with React wrappers in `web/src/ui`. A living reference with every token and component is served by the app itself at `/design`.

## Contributing

Bug reports, ideas and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). Security problems go through [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE). The licences of the third-party software that ships inside hoard are in `THIRD_PARTY_NOTICES.md`, included in every release archive and in the container image (`make notices` generates it locally).
