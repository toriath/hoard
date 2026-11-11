# Installing hoard on TrueNAS SCALE

For TrueNAS SCALE 24.10 ("Electric Eel") or newer, where apps run on Docker. Menu names are from those releases and may differ slightly in yours.

You end up with:

- hoard at `http://<truenas>:8080` on your LAN (and on your tailnet, if the NAS is on it)
- all data in one dataset that you can snapshot and replicate like any other

## Before you start

- **An apps pool is selected.** Apps → Configuration → Choose Pool. Skip if you already run apps.
- **The image exists.** GitHub Actions publishes every release as `ghcr.io/toriath/hoard:latest` (and under its version, e.g. `0.1.0`). Check the repository's Releases page.

## 1. Create the dataset

1. **Datasets** → select your pool (or a parent such as `apps`) → **Add Dataset**.
2. Name: `hoard`. Dataset Preset: **Apps**. Save.

The *Apps* preset gives the built-in `apps` user (UID 568), which hoard runs as, write access. If you used another preset: select the dataset → **Permissions → Edit**, set owner user and group to `apps`, tick *Apply User* and *Apply Group*, save.

Note the path. For pool `tank` and parent `apps` it is `/mnt/tank/apps/hoard`.

## 2. Image access

The image is public, so there is nothing to do.

Only if you run a private fork: create a classic GitHub personal access token with the single scope `read:packages` (fine-grained tokens do not work for the container registry), then in TrueNAS go to **Apps → Configuration → Sign-in to a Docker registry** and enter `https://ghcr.io`, your GitHub username and the token as password.

## 3. Install the app

1. **Apps → Discover Apps** → the **⋮** menu next to *Custom App* → **Install via YAML**.
2. Name: `hoard`.
3. Paste the contents of [`deploy/compose.yaml`](../deploy/compose.yaml) and adjust three lines:

   | Line | Change to |
   | --- | --- |
   | `- /mnt/tank/apps/hoard:/data` | the dataset path from step 1 (keep `:/data`) |
   | `TZ: Etc/UTC` | your time zone, e.g. `Europe/Berlin` |
   | `- "8080:8080"` | only if port 8080 is taken: change the **left** number |

4. Save. After the image is pulled the app shows as *Running*.

## 4. First start

1. Open `http://<truenas-ip>:8080`.
2. Create the first account, and do it right after installing: until an account exists, anyone who can reach the port can create it. (To avoid that window entirely, set `HOARD_ADMIN_USER` and `HOARD_ADMIN_PASSWORD` in the YAML for the first start and remove them afterwards.) Keep the password in your password manager: there is no password reset yet.
3. **Settings → Connect Claude**: create an API token and copy the ready-made command for Claude Code or the config for Claude Desktop. The snippets already contain the address you are browsing from.
4. Optional, **Settings → AI agent**: enter a Claude API key, or the address of your LM Studio server (`http://<that-machine>:1234/v1`, with *Serve on Local Network* enabled in LM Studio).

## 5. Reach it over your tailnet

- **The NAS is already on your tailnet** (for example through the TrueNAS Tailscale app with host networking enabled): nothing to do. Use `http://<truenas-tailnet-name>:8080`. Create the Claude snippets while browsing through that address if your Claude machine is not always on the LAN.
- **You want HTTPS and a separate name** (`https://hoard.<tailnet>.ts.net`): install [`deploy/compose.tailscale.yaml`](../deploy/compose.tailscale.yaml) instead of `compose.yaml`. It needs a Tailscale auth key, a second dataset for the Tailscale state, and MagicDNS plus HTTPS certificates enabled for the tailnet. This variant follows Tailscale's documented sidecar setup but has not been run by the maintainers yet.

Do not forward hoard to the public internet.

## 6. Back up

Everything is in the dataset from step 1: original files, a metadata sidecar per asset, the index and its daily snapshots.

**Data Protection → Periodic Snapshot Tasks → Add**, choose the dataset, for example daily with two weeks' retention. Add a replication or cloud sync task if you want a copy off the NAS.

## 7. Update

`latest` moves to each new release. Releases happen automatically when a feature or fix is merged; see the repository's Releases page for what changed.

- On the app's page under **Apps → Installed**, use the update or pull-images action if your release offers it for custom apps.
- Otherwise, in **System → Shell**:

  ```bash
  sudo docker pull ghcr.io/toriath/hoard:latest
  ```

  then open the app, **Edit → Save** to redeploy it with the new image.

The database upgrades itself on start. To pin a release instead of following `latest`, use its version in the YAML, e.g. `ghcr.io/toriath/hoard:0.1.0`, or `ghcr.io/toriath/hoard:0.1` to follow patch releases only. `edge` is the unreleased tip of `main`.

## If something goes wrong

| Symptom | Cause and fix |
| --- | --- |
| Pull fails with `unauthorized` or `denied` | Only for a private fork: the registry sign-in from step 2 is missing or the token lacks `read:packages` |
| App restarts in a loop, log says `permission denied` on `/data` | The dataset is not writable for `apps` (UID 568): fix ownership as in step 1 |
| `port is already allocated` | Another app uses 8080: change the left port number in the YAML |
| Page loads over LAN but not over the tailnet | The NAS's Tailscale app is not on host networking, or tailnet ACLs block the port |
| Uploads of large files fail | Raise `HOARD_MAX_UPLOAD_MB` in the YAML (default 4096) |

Logs: **Apps → Installed → hoard → Workloads → the log icon** next to the container.
