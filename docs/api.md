# REST API

Base path: `/api/v1`. JSON in, JSON out. Authenticate with an API token (`Authorization: Bearer hoard_…`, created under Settings → Connect Claude) or with the browser session cookie.

Errors have one shape:

```json
{ "error": { "code": "not_found", "message": "asset \"ast_x\": not found" } }
```

## Assets

| Method & path | Purpose |
| --- | --- |
| `GET /assets` | List and search. Query: `q`, `type` (comma list, any of), `tag` (comma list, all of), `collection`, `project` (id or `none`), `license` (value or `none`), `untagged=1`, `trashed=1`, `sort` (`relevance`, `created`, `updated`, `name`, `size`, `type`), `order` (`asc`, `desc`), `limit` (max 500), `offset`, `ids` |
| `POST /assets` | Create. Either a JSON body with metadata only, or `multipart/form-data` with an optional `meta` part (JSON, must come first) followed by one or more `file` parts |
| `GET /assets/{id}` | One asset with files, collections and projects |
| `PATCH /assets/{id}` | Partial update (see fields below) |
| `DELETE /assets/{id}` | Move to trash; `?permanent=1` deletes for good |
| `POST /assets/{id}/restore` | Restore from trash |
| `POST /assets/bulk` | `{ "ids": [...], "action": "" \| "trash" \| "restore" \| "purge", "patch": {…} }` |
| `POST /assets/{id}/files` | Add files (`multipart/form-data`, `file` parts). Same name replaces |
| `PUT /assets/{id}/files/{filename}` | Add one file from the raw request body: `curl -T sword.png …` |
| `DELETE /assets/{id}/files/{fileId}` | Remove a file |
| `POST /assets/{id}/files/{fileId}/primary` | Make a file the primary one |
| `GET /assets/{id}/raw/{filename}` | File content, with range support; `?download=1` forces a download |
| `GET /assets/{id}/download` | The file, or a zip when the asset has several |
| `GET /download?ids=a,b,c` | Zip of several assets, one folder each, including `asset.json` |
| `GET` / `PUT` / `DELETE /assets/{id}/thumbnail` | Preview thumbnail; `PUT` takes an image as the request body |
| `POST /assets/{id}/suggest` | Ask the AI agent for metadata without saving it |
| `POST /assets/analyze` | `{ "ids": [...], "overwrite": false }` queues the AI agent |

### Patch fields

```json
{
  "name": "Flaming Sword",
  "type": "sprite",
  "description": "…",
  "prompt": "…",
  "source": "…", "source_url": "https://…",
  "license": "CC0-1.0", "license_url": "https://…",
  "attribution": "…",
  "tags": ["replaces", "all"],
  "add_tags": ["merged"], "remove_tags": ["dropped"],
  "attrs": { "frames": 8, "old_key": null },
  "add_collections": ["col_…"], "remove_collections": ["col_…"],
  "add_projects": ["prj_…"], "remove_projects": ["prj_…"],
  "project_note": "how it is used"
}
```

Every field is optional; omitted fields stay as they are. An empty string clears a text field. In `attrs`, `null` removes a key.

### Example: upload with metadata

```bash
curl -H "Authorization: Bearer $HOARD_TOKEN" \
  -F 'meta={"tags":["pixel-art","weapon"],"license":"CC0-1.0","prompt":"a flaming sword, 32x32"};type=application/json' \
  -F file=@sword.png \
  http://truenas.local:8080/api/v1/assets
```

## Groups, tags, stats

| Method & path | Purpose |
| --- | --- |
| `GET /collections`, `POST /collections` | List, create (`name`, `description`, `kind`: `pack`, `style`, `project`, `other`) |
| `GET` / `PATCH` / `DELETE /collections/{id}` | Read, update, delete (assets are kept) |
| `GET /projects`, `POST /projects` | List, create (`name`, `description`, `url`, `status`: `active`, `released`, `archived`) |
| `GET` / `PATCH` / `DELETE /projects/{id}` | Read, update, delete (assets are kept) |
| `GET /tags?q=&limit=` | Tags with usage counts |
| `POST /tags/rename` | `{ "from": "old", "to": "new" }`; an empty `to` deletes the tag |
| `GET /stats` | Counts by type, size on disk, licenses |
| `GET /meta` | Asset types, license suggestions, limits, current user |

Membership is changed through asset patches (`add_collections`, `add_projects`, …), single or bulk.

## Activity, lineage and recipes

| Method & path | Purpose |
| --- | --- |
| `GET /activity?asset=&since=&after=&action=&via=&limit=` | The change feed. `since` is an RFC 3339 time; `after` is a sequence number and returns only newer entries, oldest first, so a client can follow the last `seq` it saw. `action` and `via` take comma-separated lists: action prefixes such as `asset.`, `request.`, `asset.review`, `note.`, `project.`, and channels `web`, `api`, `mcp`, `system`. `limit` defaults to 50, at most 500. Without `after` the newest entries come first. Returns `{ "items": [...], "latest_seq": n }`; each item has `seq`, `at`, `actor`, `via`, `action`, `asset_id`, `ref_id`, `summary`, and `asset_name` and `thumb_version` while the asset exists (`asset_gone` when it was purged). `latest_seq` is the newest entry overall, also when the filter matched nothing |
| `GET /assets/{id}/lineage` | `{ ancestors, children, siblings, supersedes, superseded_by_chain }`: compact asset records (`id`, `name`, `type`, `status`, `thumb_version`, `prompt`, `generator`). `ancestors` follows `derived_from` (nearest first, at most 20, cycle-safe); `children` and `siblings` skip trashed assets |
| `GET /assets/{id}/recipe` | Prompt, `generator`, the `derived_from` chain, `variations`, `has_recipe` and a `reproduce` hint |

Entries older than 365 days are pruned by the daily maintenance.

## Project briefs, fit, report, credits

| Method & path | Purpose |
| --- | --- |
| `GET /projects/{id}/brief` | The brief (`{}` when none). Also present as `brief` on the project |
| `PATCH /projects/{id}/brief` | Merge a JSON object into the brief: fields present replace, `null` clears, absent fields stay. `400` with the reason when validation fails |
| `GET /projects/{id}/report` | Health report: `verdict` (`ok`, `attention`, `problems`), `summary`, license problems, missing attribution, placeholders, assets needing work, fit issues, size against budget, open requests, `repo_drift` (only with a repository snapshot) |
| `GET /projects/{id}/credits` | `{ "markdown", "asset_count", "needs_attention": [...] }` |
| `GET /projects/{id}/credits.md` | The same Markdown as `text/markdown`, as an attachment named `CREDITS.md` |
| `GET /projects/{id}/bundle` | Zip: one folder per asset (files and `asset.json`) plus `CREDITS.md`; rejected and superseded assets left out |
| `POST /assets/{id}/fit` | Fit check. Body `{ "project_id": "…" }` for one project, empty for every project the asset is used in. Returns `{ "reports": [{ "project_id", "project_name", "has_brief", "errors", "warnings", "issues": [{ "severity", "code", "message" }] }] }`. A `POST` because it stores measured audio attributes when absent |

## Accounts and settings

| Method & path | Purpose |
| --- | --- |
| `GET /auth/status` | Whether setup is required and who is signed in |
| `POST /auth/setup` | Create the first account (only while none exists) |
| `POST /auth/login`, `POST /auth/logout` | Browser session |
| `POST /auth/password` | `{ "current": "…", "new": "…" }` |
| `GET /users`, `POST /users`, `DELETE /users/{id}` | Manage accounts |
| `GET /tokens`, `POST /tokens`, `DELETE /tokens/{id}` | Manage API tokens; the secret is returned once on creation |
| `GET` / `PUT /settings/ai` | AI agent settings. The API key is write-only, is dropped when provider or base URL change, and `"clear_api_key": true` removes it |
| `POST /settings/ai/test`, `POST /settings/ai/models` | Test the provider; list a server's models (`{ "base_url": "…", "api_key": "…" }`, both optional) |

Managing accounts, tokens and the AI provider needs a browser session: `POST`/`DELETE /users`, `POST`/`DELETE /tokens`, `POST /auth/password` and every write under `/settings/ai` answer `403 session_required` to an API token. Everything else works with either.

## Signed links

MCP tools hand out links that need no token and expire after two hours:

- `PUT /upload/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl-zip?ids=…&exp=…&sig=…`
- `GET /dl-bundle/{projectId}?exp=…&sig=…`: the project bundle (assets and `CREDITS.md`)

A signature is bound to its expiry and to one file name of one asset (for a zip, to one exact list of assets; for a bundle, to one project). Upload links cannot write into a trashed asset. Revoking the API token that requested a link does not revoke the link.

## Other endpoints

- `GET /healthz` (no authentication): `{ "status": "ok" }`. The version is in `GET /meta`, for signed-in callers.
- `POST /mcp`: the MCP endpoint (Streamable HTTP, stateless, bearer token required)
