# hoard

[![CI](https://github.com/toriath/hoard/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/toriath/hoard/actions/workflows/ci.yml)
[![Security](https://github.com/toriath/hoard/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/toriath/hoard/actions/workflows/security.yml)
[![Release](https://img.shields.io/github/v/release/toriath/hoard)](https://github.com/toriath/hoard/releases)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

A self-hosted database for game assets: sprites, 3D models, textures, SFX, music, VFX, shaders, video, fonts and whatever else a game needs. One small container, one data directory, a web UI for people and an MCP server for Claude.

- **AI first.** Every feature is available to Claude Code and Claude Desktop over MCP with an API token: search, download into a project, store newly generated assets with their prompts, tag, group, track usage.
- **For humans too.** A fast web UI for desktop and phone with previews for every media kind (sprite sheets play as animations once their frame size is set under *Edit → Animation*), search, sorting, bulk actions and downloads.
- **Your data, readable.** Original files are stored untouched next to a JSON sidecar per asset. No lock-in, trivial backups.
- **Built-in AI agent** that names, describes and tags assets, using the Claude API or a local model in LM Studio.

## Contents

- [Run it](#run-it)
- [Install on TrueNAS SCALE](#install-on-truenas-scale)
- [Reach it over your tailnet](#reach-it-over-your-tailnet)
- [Connect Claude (MCP)](#connect-claude-mcp)
- [The built-in AI agent](#the-built-in-ai-agent)
- [Working on a game with agents](#working-on-a-game-with-agents)
- [Project briefs and health](#project-briefs-and-health)
- [Asset requests and review](#asset-requests-and-review)
- [Keeping a game repository in sync](#keeping-a-game-repository-in-sync)
- [History, recipes and tags](#history-recipes-and-tags)
- [Duplicates, similar assets and colours](#duplicates-similar-assets-and-colours)
- [Importing a pack](#importing-a-pack)
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

### Tool reference

Every MCP tool appears once below, grouped by job. Five MCP prompts (listed after the tables) walk through common jobs, and the project brief is also available as the resource template `hoard://project/{id}/brief` (JSON).

**Library basics**

| Tool | Purpose |
| --- | --- |
| `hoard_overview` | Counts, valid types, license suggestions, collections, projects, top tags |
| `search_assets` | Full-text search with filters (type, tags, collection, project, license, dominant colours) and paging |
| `get_asset` | Full record plus download commands |
| `view_asset` | Look at the preview image, or read a text file such as a shader |
| `show_assets` | Show assets to you as an interactive gallery to look, listen and pick (see above) |
| `asset_media` | Used by the gallery to play audio; of no use to an agent |
| `create_asset` | Create with metadata; returns upload commands (or takes small text files inline) |
| `get_upload_commands` / `get_download_commands` | Signed `curl` commands for files, single or zipped |
| `update_asset` / `bulk_update_assets` | Edit metadata, tags, collections and project usage |
| `delete_assets` / `restore_assets` | Trash (recoverable) or permanent delete |
| `list_tags` / `rename_tag` | Tag vocabulary maintenance |
| `list_collections` / `save_collection` / `delete_collection` | Packs, styles and other groups |
| `list_projects` / `save_project` / `delete_project` | Projects that assets are used in |
| `analyze_assets` | Run the built-in AI agent |

**Project briefs and health** (see [Project briefs and health](#project-briefs-and-health))

| Tool | Purpose |
| --- | --- |
| `get_project_brief` / `set_project_brief` | Read and change what a project needs from its assets (engine, palette, sizes, formats, audio, licenses, repository folders) |
| `check_asset_fit` | Check up to 50 assets against a project's brief, with concrete findings |
| `project_report` | Health of a project: license problems, placeholders, assets that do not fit, size budget, open requests, drift against the repository |
| `get_project_credits` | CREDITS.md for a project, plus a signed command that downloads every asset with it |

**Asset requests and review** (see [Asset requests and review](#asset-requests-and-review))

| Tool | Purpose |
| --- | --- |
| `request_asset` | File a request: title, description, type, spec (size, frames, duration, file name), priority, project; optionally with a placeholder |
| `list_requests` / `get_request` | The queue, filtered; one request with candidates, feedback, project brief and the asset to rework |
| `claim_request` | Take a request (by id, or the best open one matching `kind`, `asset_types`, `project_id`) and get the complete work item |
| `renew_claim` / `release_request` | Extend the lease; give the request back |
| `submit_request` | Hand in one or more created assets as candidates |
| `cancel_request` | Withdraw a request |
| `make_placeholder` | PNG or WAV stand-in for a request, with download commands |
| `review_asset` | `approve`, `reject` or `rework` an asset (rework files a request) |
| `review_submission` | `accept` or `reject` a candidate of a request |
| `add_note` / `list_notes` / `resolve_note` | Comments and feedback on assets |

**Game repository and delivery** (see [Keeping a game repository in sync](#keeping-a-game-repository-in-sync))

| Tool | Purpose |
| --- | --- |
| `get_repo_audit_command` | One shell command that uploads a hash list of the game repository's asset files to hoard |
| `audit_project` | Compare that list with the library: known, outdated, untracked, missing, not linked |
| `sync_project` | Download commands for missing or changed files, placed by the project brief; lock file; removals |
| `pack_atlas` | Pack the main images of several assets into one atlas (Godot, Phaser or generic JSON) |
| `get_derivative_command` | A resized copy of an image (nearest or linear, PNG or JPEG), rendered on demand |
| `get_engine_hints` | Import settings for Godot 4, Unity and Phaser; Godot `SpriteFrames` for sprite sheets |

**History and recipes** (see [History, recipes and tags](#history-recipes-and-tags))

| Tool | Purpose |
| --- | --- |
| `recent_changes` | What changed since a time or a sequence number: who did what to which asset, so an agent resuming work sees what people and other agents did. Returns `latest_seq` for polling with `after_seq` |
| `get_recipe` | Prompt, generator recipe and `derived_from` chain of an asset, with a hint on how to regenerate or vary it |

**Library maintenance** (see [Duplicates, similar assets and colours](#duplicates-similar-assets-and-colours) and [Importing a pack](#importing-a-pack))

| Tool | Purpose |
| --- | --- |
| `find_similar` | Duplicates and look-alikes of one asset, with scored reasons ("identical file", "visually near-identical (distance 3)", "shares 4 tags") |
| `find_duplicates` | Groups of identical files and near-identical pictures across the whole library |
| `get_import_command` | A signed `curl -T pack.zip` command that imports a zip archive as a pack |

**Prompts.** `work-request-queue`, `request-missing-assets`, `review-submissions`, `release-check` and `tidy-library` are registered as MCP prompts; Claude Code offers them as slash commands (`/mcp__hoard__<name>`). They are described under [Asset requests and review](#asset-requests-and-review).

The same token works for the REST API under `/api/v1`; see [`docs/api.md`](docs/api.md).

## The built-in AI agent

Configure it under **Settings → AI agent**. It looks at an asset's preview image, its files and its existing metadata and proposes a name, a description, tags and a type. By default it only fills gaps: tags are merged, a description you wrote stays. Use it per asset (*AI fill*, or *Suggest with AI* while editing), on a selection (*AI tag*), or automatically on upload.

- **Claude API.** Paste an API key. The default model is `claude-opus-5-5`; `claude-haiku-5-5` is far cheaper and usually good enough for tagging.
- **Local model.** Any OpenAI-compatible server. For LM Studio: Developer → Local Server → start, enable *Serve on Local Network*, and enter `http://<that-machine>:1234/v1`. Inside the container `localhost` is the container itself, so use the LAN or tailnet address. Choose a vision-capable model if previews should be analysed; text-only models still work from file names and metadata.

The agent sees the asset's preview image; where previews come from is described under [What an asset holds](#what-an-asset-holds).

## Working on a game with agents

hoard is meant to sit between the agents that build a game and the agents that make its assets. The pieces below fit together like this; each has its own section.

1. **Brief.** A project carries a brief: engine, palette, sizes, formats, licenses and repository folders. Agents read it before they pick or create assets ([Project briefs and health](#project-briefs-and-health)).
2. **Requests and placeholders.** A coding agent that needs an asset hoard does not have files a request, optionally with a placeholder it drops into the game so everything runs meanwhile ([Asset requests and review](#asset-requests-and-review)).
3. **Generator agents.** A second agent, connected to hoard and to an image or audio tool, claims requests, generates, stores the result with its prompt and recipe, and submits it for review.
4. **Review and steering.** You accept or reject submissions, or approve, reject or send back any asset for rework; the feedback goes into the next request.
5. **Repository sync.** An audit of the game repository compares what the game contains with the library, and a sync places new and changed files according to the brief ([Keeping a game repository in sync](#keeping-a-game-repository-in-sync)).
6. **Health and credits.** The project report and `CREDITS.md` show license problems, placeholders still in use, assets that do not fit and open requests before a release.

Throughout, the activity feed and recipes ([History, recipes and tags](#history-recipes-and-tags)) let an agent that resumes work see what people and other agents did, and reproduce or vary an asset.

## Project briefs and health

A project can carry a **brief**: what the game needs from its assets. Agents read it before they create or pick assets, and hoard checks assets against it. Set it on the project page (**Brief → Set up**) or with `set_project_brief`. Every field is optional; an empty brief means nothing is known and nothing is checked.

| Field | Meaning |
| --- | --- |
| `engine`, `engine_version` | `godot`, `unity`, `unreal`, `phaser`, `bevy`, `love2d`, `gamemaker`, `web` or `custom` |
| `art_direction`, `notes` | Free text for people and agents |
| `pixel_art`, `palette` | Pixel art flag; palette as `#rrggbb` colours |
| `tile_size`, `base_resolution` | Tile edge in px; `320x180` |
| `max_texture_size`, `power_of_two` | Longest edge in px; whether textures must be power-of-two |
| `formats` | Accepted file extensions per asset type, lowercase without dot: `{"sprite": ["png"], "sfx": ["wav", "ogg"]}` |
| `audio` | `sample_rate`, `channels`, `loudness_lufs`, `max_peak_dbfs` |
| `licenses` | Allowed licenses (empty: any) |
| `folders` | Repository folder per asset type, plus `default`: `{"sprite": "assets/sprites"}` |
| `naming` | `snake_case`, `kebab-case`, `camelCase`, `PascalCase` or `keep` |
| `size_budget_mb` | Budget for the assets of the project |

Saving merges: a field you send replaces the stored value as a whole, `null` clears it, fields you leave out stay. The result is validated (hex colours, positive numbers, extensions without dot, folders that are relative and free of `..`) and limited to 32 KiB.

**Fit check.** `check_asset_fit`, and the *Fit* section of the asset drawer, compare an asset with the brief of a project and report errors, warnings and information with concrete numbers (`44100 Hz, project wants 48000 Hz.`). It checks the accepted formats, image size, power-of-two and tile size, how much of an image is off the palette, sample rate and channels of WAV, FLAC and Ogg files, the peak level of WAV files, allowed licenses, attribution for CC-BY licenses, review status and size. Two limits to know:

- The loudness it reports is an estimate from a PCM WAV file: the summed energy of all channels over the non-silent 100 ms blocks, minus 0.691 as in ITU-R BS.1770. It is not true LUFS (no K-weighting), so it only flags clear misses (more than 3 dB), and for sound effects only as information. Peak and loudness are not measured for Ogg, FLAC or MP3; for those only the header is read.
- Palette conformity decodes the image with the same memory limit and one-at-a-time rule as thumbnails; pixels within 16 RGB units of a palette colour count as on the palette, and transparent or anti-aliased edge pixels are ignored.

A fit check also stores the measured audio values (`duration_s`, `sample_rate`, `channels`, `bit_depth`, `peak_dbfs`) in the asset's attributes when it has none yet, or when the primary file changed since (`measured_sha256`). Trashed assets are skipped. That does not change the asset's "changed" time or the activity feed.

**Project report.** It never decodes or writes anything, so the project page stays cheap: image palette is not part of it, and audio is judged from the values stored by an earlier fit check (and says "not measured yet" otherwise). The project page shows a health section and `project_report` returns it: a verdict (`ok`, `attention`, `problems`), assets with license problems or missing attribution, placeholders still in use, assets in rework, rejected or superseded, assets that do not fit, size against the budget, open requests, and assets whose files are missing from the last repository snapshot (only when one exists).

**Credits.** *Credits* on the project page (and `get_project_credits`) writes a `CREDITS.md`: assets grouped by license with author, source and license links, and a *needs attention* list for assets without a license or with CC-BY and no attribution. All stored text is escaped, so a name cannot inject Markdown or HTML. The bundle download is a zip with one folder per asset (as in the other zip downloads) and `CREDITS.md` at the root; rejected and superseded assets are left out. The fit checks, report and credits are verified by tests with generated images and WAV files; the Ogg and FLAC header readers only against hand-built headers, not against files from real encoders.

## Asset requests and review

hoard can run a small work queue between the agents that build your game and the agents that make its assets. Nobody has to wait for anybody:

1. A **coding agent** finds that the game needs a coin sprite or a door sound that hoard does not have, and files a **request** (`request_asset`). It can ask for a **placeholder** at the same time: a flat-colour PNG of the right size, or a quiet beep of the right length, which it drops into the game so everything runs meanwhile.
2. A separate **generator agent**, for example a second Claude Code session connected to hoard and to your image tool (Draw Things) or audio tool (the audio-studio MCP), **claims** the next open request (`claim_request`). It gets everything it needs in one answer: the request, the project brief (art direction, palette, sizes, formats), for a rework the existing asset with its prompt, recipe and your feedback, and the exact next steps. It generates, stores the result with `create_asset` (prompt and generator recipe included), uploads the files and **submits** them (`submit_request`).
3. **You review** in the web UI (**Requests**, column *Review*) or in a conversation. **Accept** makes the candidate the result: the asset is approved, the request is done, and the placeholder (or the asset that was being reworked) is marked superseded; its project usage, with notes, moves to the new asset. **Reject** with feedback sends the request back to the queue, and the next claim shows your feedback.
4. You can also **steer any asset** at any time: *Approve*, *Needs rework* or *Reject* in the asset drawer, or `review_asset` over MCP. *Needs rework* stores your feedback as a note, sets the status to `rework` and puts a rework request into the queue, with the asset's type, size, prompt and recipe copied in, so a generator can redo it from the request alone.

A claim is a lease (60 minutes by default, at most 24 hours). An agent that dies simply stops renewing it, and the request is open again.

**Give each generator agent its own token.** hoard tells claimers apart by the authenticated identity (user, channel and token name); the optional `agent` label of `claim_request`, `renew_claim`, `release_request` and `submit_request` only distinguishes claims of agents sharing a token, it does not count for review.

**Nobody reviews their own work.** hoard refuses to accept or reject a submission, or to approve or reject an asset that is still under review, when the caller is the identity that submitted it ("a submission cannot be reviewed by the agent that made it; ask the user"). The browser session, the user, can always review; so can a different agent when you ask your coding agent to. Superseded assets are reviewed through their replacement, and placeholders cannot be approved: they are replaced by accepting a submission.

### Setting up a generator agent

Give a second agent the hoard MCP server and the tools that make the assets. In Claude Code, the prompt `work-request-queue` is available as a slash command; its arguments say what you generate with and what types of request to take:

```
/mcp__hoard__work-request-queue tool="Draw Things" asset_types="sprite,ui,texture"
```

The agent then claims requests one by one until the queue is empty for it. Review stays with you: a generator agent never accepts its own work.

The other prompts: `request-missing-assets` (for the coding agent: scan the game for missing assets, request them with specs from the project brief, place placeholders), `review-submissions` (walks you through the pending candidates with `show_assets`), `release-check` (project report, licences and credits, placeholders still in use; uses the project tools described elsewhere in this README) and `tidy-library`.

The request and review tools are listed in the [tool reference](#tool-reference). Titles, descriptions, specs, notes and feedback are data written by people and agents. The tools return them in named fields and say so; agents are told to treat them as requirements, not as commands.

Placeholders exist for `sprite`, `ui`, `tileset`, `texture` and `vfx` (a PNG, `width` × `height` from the spec, or the sheet size from `frame_width`, `frame_height`, `frames`, `columns`; default 64×64, at most 4096) and for `sfx`, `music` and `voice` (a 16-bit mono WAV of `duration_s`, default 1 s, at most 30 s). Other types have none. The placeholder is an ordinary asset with status `placeholder`, named `[placeholder] <title>`, and part of the request's project.

Nothing here has been run against a real image or audio generator, nor have the prompts been tried in a real Claude Code session; the loop is tested end to end with the official MCP client.

## Keeping a game repository in sync

hoard never reads your game repository. An agent (or you) runs one command in it; the command sends a list of file names, sizes and SHA-256 hashes to hoard, and everything else is computed from that list. File contents never leave the repository this way.

1. `get_repo_audit_command` (project, optional `root`) returns a POSIX shell command: it lists the asset-like files below the root (images, audio, models, fonts, video and other formats hoard knows; not code or text; `.git`, `node_modules`, `.godot`, `Library`, `Temp` and build folders are skipped), hashes them with `sha256sum` or `shasum -a 256`, and uploads the list through a signed link bound to the project. The link is valid for two hours. Files with control characters or invalid UTF-8 in their name are left out (the upload reports how many). `build`, `dist`, `target`, `Library` and similar folders are skipped only directly below the root. Pass an absolute root. A new upload replaces the project's previous snapshot. In the web UI the project page has the same command under **Game repository**.
2. `audit_project` compares the snapshot with the library. Files whose content matches an asset are *known* (flagged when the asset was superseded by a newer version, has status rework, rejected or placeholder, or is not linked to the project); the others are *untracked*, with a suggested type and name for `create_asset`. Project assets without any file in the repository are *missing*.
3. `sync_project` decides where every file of the project's assets belongs and returns `curl` download commands only for what is missing or differs. Superseded assets are followed to their newest version. If the repository holds an older version of an asset, the new file is written to the same path (same extension required), whatever its file name; otherwise the path comes from the project brief: `folders[<asset type>]`, else `folders.default`, else `assets/<type>`, with the file name converted to the brief's `naming` (`snake_case`, `kebab-case`, `camelCase`, `PascalCase` or `keep`). A different file already sitting at a target path is reported as a `conflict` and not overwritten. Repository files of superseded assets that were *not* replaced in place are listed as `removals`: hoard never deletes anything. The result also carries a command for `hoard.lock.json` (project, and per file: path, asset, SHA-256, size, license, attribution), which is also available as **Lock file** on the project page.

Download commands for missing files never overwrite a file that already exists. Run the audit again after a sync to confirm. Without a brief, files go to `assets/<type>/` under their original names.

Other delivery tools:

- `pack_atlas` packs the main images of up to 256 assets (PNG, JPEG, GIF, WebP, BMP, TIFF) with MaxRects into one PNG of at most 8192 px, with optional padding, edge extrusion and power-of-two size. Output is a zip: `atlas.png` plus `atlas.json` (generic frames, or the Phaser JSON-hash format) or, for Godot 4, one `AtlasTexture` `.tres` per sprite. The same dialog is available in the Library: select assets and press **Atlas**.
- `get_derivative_command` renders a scaled copy of an image on demand (`width`/`height` or `scale`, up to 8192 px per edge) and caches it in `<data>/cache/derived/`. The filter defaults to nearest for pixel art (asset attribute `pixel_art`, a `pixel-art` tag, or a brief with `pixel_art`) and for integer scales of 2 or more. Output is PNG or JPEG; WebP is not supported. The link covers every parameter.
- `get_engine_hints` recommends import settings per file for Godot 4, Unity or Phaser, from the asset and the brief (pixel art, tile size, loops). For sprite sheets with `frame_width` and `frame_height` attributes it generates a Godot `SpriteFrames` resource. These are recommendations from general engine knowledge and have not been checked in a running engine.

## History, recipes and tags

- **Activity.** Every change to an asset, request, review, note or project is recorded with who made it (a person in the web UI, a script with an API token, an agent over MCP). The **Activity** page groups the feed by day, filters by kind and by people or agents, refreshes every 30 seconds and links to the assets. The drawer of an asset has a collapsed *History* section with its last ten changes. Entries older than 365 days are removed by the daily maintenance. Agents read the same feed with `recent_changes`.
- **Lineage and recipes.** An asset can record the asset it was made from (`derived_from`) and a structured generator recipe (tool, model, seed, steps, cfg, size, negative prompt…). The drawer shows the recipe with copy buttons for prompt and seed, plus the parent, variations and replacements as thumbnails. `get_recipe` gives an agent the same, with a hint on how to reproduce the asset or make a variation.
- **Generator metadata import.** When a file is added, hoard looks for the recipe the generator left behind and fills prompt and recipe where they are still empty (never overwriting what is there): Automatic1111 / Forge `parameters` text and ComfyUI `prompt`/`workflow` chunks in PNG files, Draw Things metadata (see the caveat in `CLAUDE.md`), and the `.json` sidecar that audio-studio writes next to a sound file. Failures are ignored; an upload never fails because of this. An imported recipe shows up in the activity feed as *generator metadata imported*.
- **Tags.** The **Tags** page lists all tags with counts, searches and sorts them, renames them (asking before merging into an existing tag), deletes them, merges several selected tags into one and opens the library filtered by a tag.

## Duplicates, similar assets and colours

For every picture the server computes a 64-bit perceptual hash (a difference hash of the decoded image, no second decode) and up to five dominant colours. Pictures stored before this existed are indexed in the background after the server starts.

- **Upload warnings.** The result of an upload lists `duplicate_of` (identical bytes) and `similar_to` (near-identical pictures: the same image re-encoded, rescaled or lightly touched, at most 5 of 64 hash bits apart).
- **Similar assets.** The asset drawer lists look-alikes with the reason for each; `find_similar` and `GET /assets/{id}/similar` return the same. Non-pictures are matched by identical files and by the same type with overlapping tags.
- **Duplicates view.** *Find duplicates…* in the Library's view menu (or `find_duplicates`, `GET /duplicates`) lists groups of identical files and of near-identical pictures, where you can open them and move surplus ones to the trash.
- **Colour search.** The Library's filter dialog has colour swatches; `search_assets` takes `colors` and the REST list takes `color=ff3c3c`. A colour is matched to the nearest of 53 palette colours (12 hues in four variants plus greys) and an asset matches when that colour makes up at least 10 % of its opaque pixels. Several colours (at most 10) must all be present.

The hash and colours describe the primary file of an asset when it is an image the server can decode (PNG, JPEG, GIF, WebP, BMP, TIFF). SVG and AVIF pictures have neither.

## Importing a pack

Drop a single `.zip` on the web UI and choose **Import as pack**, or let an agent call `get_import_command` for a `curl -T pack.zip` command (`POST /import` does the same for scripts). hoard unpacks the archive on the server and creates a collection of kind pack, named after the zip or `collection_name`, with one of three layouts: `per_file` (one asset per file, the default), `per_folder` (one asset per top-level folder; files in sub-folders keep their path as `sub_folder_file.png`; root files stay separate) or `single`. Tags and a project can be given for all assets.

The licence is read from `LICENSE*`, `COPYING*`, `README*`, `CREDITS*` and `.txt`/`.md` files in the archive root (CC0, CC-BY, CC-BY-SA, CC-BY-NC with their versions, OFL, MIT, Apache-2.0 and public domain, by title, wording or creativecommons.org link; CC0 only by its own title or link, because every Creative Commons licence text mentions the CC0 dedication), together with a line such as "Created by …" or "© …" as attribution, and applied to every asset unless you pass a licence. Negated mentions ("not public domain") do not count, and when the files name several different licences none is applied and the summary lists them. Those files (`LICENSE`, `README`, `CREDITS`… with a `.txt`, `.md`, `.rst`, `.html`, `.pdf` or no extension) are never imported as assets in any mode; the summary names them. Detection is by keywords, not legal advice: read the summary and correct it. The summary also lists skipped and failed files.

The archive is treated as hostile: at most 10 000 entries (the count in the zip's end record is checked before the directory is read), an archive of at most the upload limit and 2 GiB, at most the upload limit unpacked, no entry that compresses more than 200:1 once it is larger than 4 MiB (a zip bomb), no symlinks, encrypted or empty entries; `__MACOSX`, dotfiles and folders are skipped; entry paths never reach the disk, only a sanitised file name does. The AI agent is queued for the assets of an import only when there are at most 50; for larger packs the summary says so and `analyze_assets` does the rest. A signed import link works once.

## What an asset holds

| Field | Notes |
| --- | --- |
| ID | Stable, short, URL-safe: `ast_7k2m9q4x1c8v` |
| Name, description | Free text |
| Type | sprite, tileset, ui, texture, model, animation, vfx, shader, sfx, music, voice, video, font, other. Guessed from the first file, always editable |
| Tags | Lowercase kebab-case, with autocomplete in the web UI; rename and merge on the Tags page, over MCP and REST |
| Prompt | The prompt the asset was generated with |
| Source, source URL | Generator, marketplace, artist |
| License, license URL, attribution | SPDX ids suggested, free text allowed |
| Files | One or many (a model with its textures, a sheet with its atlas). One is primary and drives the preview |
| Attributes | Free-form technical data. Dimensions, duration, triangle count are filled in automatically |
| Collections | Logical groups: pack, style, project set, other |
| Projects | Where the asset is used, with an optional usage note |

**Previews.** Previews are made where they can be. The server renders thumbnails for images it can decode (PNG, JPEG, GIF, WebP, BMP, TIFF), a waveform for WAV, MP3 and FLAC, and a specimen ("Aa Bb Cc", digits, the font's name) for TrueType and OpenType fonts, all without ffmpeg. For audio it decodes the first ten minutes, skips files above 64 MiB and work that takes more than 30 seconds, and records duration, sample rate, channels, bit depth and peak level as attributes when the asset does not have them yet; they follow the primary file when it changes. Ogg Vorbis files get only sample rate, channels and length read from their headers and no waveform, because the pure-Go Vorbis decoders can be made to loop forever by a hostile file. The server makes a thumbnail only when the asset has none; a thumbnail from the browser or from you is never replaced. Video, 3D models, Ogg waveforms, WOFF/WOFF2 fonts, Opus and other audio, and image formats the server cannot decode (SVG, AVIF) still get theirs from the browser the first time they appear in the web UI, and the AI agent sees that image only then; assets of those kinds added purely over MCP have no preview until someone opens the library once.

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

A ZFS snapshot of the dataset is a complete backup. The sidecars are a safety net and a way to use the library without hoard: if the index were lost, every asset would still be there with its metadata. If `hoard.db` is missing (or holds no assets) while `assets/*/asset.json` files exist, hoard imports them in the background at startup: assets with their ids, times, status, lineage, generator, attributes and tags, the files that are really on disk (size and SHA-256 are recomputed from the file, not taken from the sidecar), and the collections and projects they name. Asset requests, notes, the activity feed, the project brief and repository snapshots are not in the sidecars and are not restored. Accounts, API tokens and settings live only in the index, so after a rebuild you set up the first account again and create new tokens. Sidecars that cannot be read are skipped with a warning in the log, and symbolic links are never followed (not for an asset folder, its `files` folder, a file or a thumbnail). Recorded times are clamped to a sane range and text fields are cut to the usual limits. The import runs in the background; the library shows a note while it runs, and if hoard restarts in the middle it continues at the next start.

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
| `HOARD_TRUSTED_PROXIES` | – | Comma-separated IPs and CIDRs of reverse proxies in front of hoard (e.g. `172.18.0.0/16`). Requests from them are throttled per client address taken from `X-Forwarded-For` (the right-most address that is not a trusted proxy) instead of all sharing the proxy's address. Leave empty when clients connect directly: the header is then ignored |
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
