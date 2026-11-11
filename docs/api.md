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
| `PUT /assets/{id}/technical` | `{ "attrs": { "duration_s": 2.5 } }`: records attributes measured by a client while rendering a preview. Only `duration_s`, `sample_rate`, `channels`, `width`, `height` and `triangles` (non-negative numbers) are accepted. Not an edit: the asset's `updated_at` and the activity feed are left alone |
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

## Signed links

MCP tools hand out links that need no token and expire after two hours:

- `PUT /upload/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl/{assetId}/{filename}?exp=…&sig=…`
- `GET /dl-zip?ids=…&exp=…&sig=…`

A signature is bound to its expiry and to one file name of one asset (or, for a zip, to one exact list of assets). Upload links cannot write into a trashed asset. Revoking the API token that requested a link does not revoke the link.

## Other endpoints

- `GET /healthz` (no authentication): `{ "status": "ok" }`. The version is in `GET /meta`, for signed-in callers.
- `POST /mcp`: the MCP endpoint (Streamable HTTP, stateless, bearer token required)
