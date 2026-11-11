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

## Game repository delivery

Paths below are relative to `/api/v1`. All need a session or API token except the signed links.

| Method & path | Purpose |
| --- | --- |
| `GET /projects/{id}/repo/audit` | Audit of the stored repository snapshot: `counts`, `known`, `unknown`, `missing`, `unlinked` (lists cut at 500, `truncated`) |
| `GET /projects/{id}/repo/audit-command?root=…&extensions=png,ogg` | `{ "command": "…" }`: the shell command that uploads a snapshot. It contains a signed link, so treat the response like a credential |
| `GET /projects/{id}/repo/sync` | The sync plan as JSON: `entries` (path, reason `missing`/`changed`/`unchanged`/`conflict`, asset, hash), `removals`, `warnings` |
| `GET /projects/{id}/repo/lock` | `hoard.lock.json` as a download |
| `GET /atlas/pack?ids=…&padding=&max_size=&power_of_two=&extrude=&format=json\|phaser\|godot&res_path=` | Packed atlas as a zip (`atlas.png` + metadata) |
| `GET /engine-hints?ids=…&engine=&project_id=&res_dir=` | Import recommendations per asset |

Signed links (valid two hours, no token needed), handed out by the MCP tools:

- `PUT /projects/{id}/repo/snapshot?exp=…&sig=…`: body of lines `sha256<TAB>size<TAB>path`, at most 8 MiB and 50 000 lines. Paths must be relative, valid UTF-8, at most 1024 bytes, without NUL or control characters and without `.`/`..`/empty segments. The whole upload is rejected on the first bad line (`400`); a valid one replaces the project's snapshot in one transaction. The signature is bound to the project.
- `GET /projects/{id}/repo/lock-file?exp=…&sig=…`: the lock file, bound to the project.
- `GET /atlas?ids=…&padding=…&max_size=…&power_of_two=…&extrude=…&format=…&res_path=…&exp=…&sig=…`: the signature covers the asset ids (in order) and every option.
- `GET /derive/{assetId}/{filename}?width=&height=&scale=&filter=nearest\|linear&format=png\|jpeg&quality=&exp=…&sig=…`: the signature covers the asset, the file and every parameter; changing one gives `403`.

## Signed links

MCP tools hand out links that need no token and expire after two hours:

- `PUT /upload/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl-zip?ids=…&exp=…&sig=…`

A signature is bound to its expiry and to one file name of one asset (or, for a zip, to one exact list of assets). Upload links cannot write into a trashed asset. Revoking the API token that requested a link does not revoke the link.

## Other endpoints

- `GET /healthz` (no authentication): `{ "status": "ok" }`. The version is in `GET /meta`, for signed-in callers.
- `POST /mcp`: the MCP endpoint (Streamable HTTP, stateless, bearer token required)
