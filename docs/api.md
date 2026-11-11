# REST API

Base path: `/api/v1`. JSON in, JSON out. Authenticate with an API token (`Authorization: Bearer hoard_…`, created under Settings → Connect Claude) or with the browser session cookie.

Errors have one shape:

```json
{ "error": { "code": "not_found", "message": "asset \"ast_x\": not found" } }
```

## Assets

| Method & path | Purpose |
| --- | --- |
| `GET /assets` | List and search. Query: `q`, `type` (comma list, any of), `tag` (comma list, all of), `collection`, `project` (id or `none`), `license` (value or `none`), `color` (comma list of hex colours such as `ff3c3c`; each must be a dominant colour), `untagged=1`, `trashed=1`, `sort` (`relevance`, `created`, `updated`, `name`, `size`, `type`), `order` (`asc`, `desc`), `limit` (max 500), `offset`, `ids` |
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
| `GET /assets/{id}/similar` | Look-alikes, best first: `{ "items": [{ "asset": {…}, "score": 0.94, "reasons": ["visually near-identical (distance 2)"], "distance": 2 }] }`. Reasons: `identical file`, `visually near-identical (distance n)` (n ≤ 5), `visually similar (distance n)` (n ≤ 10), `shares n tags`. `limit` (default 20, max 100) |
| `GET /duplicates` | `{ "groups": [{ "kind": "identical" \| "near-identical", "reason": "…", "assets": [{…}] }] }`: up to 200 groups, identical ones and larger groups first, at most 30 assets per group |
| `POST /import` | Import a zip archive as a pack. `multipart/form-data` with an optional `options` part (JSON, must come first) and one `file` part. See below |

Upload responses (`POST /assets`, `POST /assets/{id}/files`, and the body of `PUT` upload links) report `duplicate_of` (assets holding byte-identical files) and `similar_to` (assets whose picture is within 5 bits of the new one's perceptual hash) when there are any.

### Import options

```json
{ "mode": "per_file", "collection_name": "Kenney UI", "license": "CC0-1.0", "tags": ["ui"], "project_id": "prj_…" }
```

All fields are optional. `mode` is `per_file` (default), `per_folder` or `single`; `license` overrides the licence detected in the archive. The response (`201`) holds `summary`, `collection_id`, `collection_name`, `license`, `license_source` (the file it was read from, or `options`), `attribution`, `assets` (`id`, `name`, `files`), `files`, `skipped`, `skipped_sample` and `failed` (`name`, `error`). An archive with more than 10 000 entries, one that unpacks to more than the upload limit, or one with an entry compressing more than 200:1 above 4 MiB is refused with `400 invalid` before anything is created.

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
- `PUT /import/{filename}?o=…&exp=…&sig=…`: imports the zip in the request body as a pack. `o` is the URL-safe base64 of the import options, and the signature covers the file name and `o`, so a link cannot be reused with other options. The response is the import summary.

A signature is bound to its expiry and to one file name of one asset (or, for a zip, to one exact list of assets). Upload links cannot write into a trashed asset. Revoking the API token that requested a link does not revoke the link.

## Other endpoints

- `GET /healthz` (no authentication): `{ "status": "ok" }`. The version is in `GET /meta`, for signed-in callers.
- `POST /mcp`: the MCP endpoint (Streamable HTTP, stateless, bearer token required)
