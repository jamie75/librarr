# rTorrent / ruTorrent Phase 2

Librarr supports rTorrent submission and completion monitoring through the
XML-RPC endpoint. It does not scrape ruTorrent. Submitted torrents are tracked
by the configured client ID and info hash, mapped to Librarr's synchronized
path, and imported through the normalized import engine. Removal and data
deletion are intentionally not implemented; torrents remain available for
seeding.

## Configuration

Configure `RTORRENT_URL` with the XML-RPC endpoint already exposed by the
seedbox. Keep credentials in the separate fields; credential-bearing URLs are
rejected. The Settings page also supports saving and testing the connection.

| Variable | Default | Purpose |
|---|---:|---|
| `RTORRENT_ENABLED` | `false` | Enables rTorrent |
| `RTORRENT_NAME` | `rTorrent` | Display name |
| `RTORRENT_URL` | | HTTP/HTTPS XML-RPC endpoint |
| `RTORRENT_USER` | | Optional HTTP basic-auth username |
| `RTORRENT_PASS` | | Optional HTTP basic-auth password |
| `RTORRENT_TIMEOUT_SECONDS` | `10` | RPC request timeout |
| `RTORRENT_LABEL_FIELD` | `d.custom1=` | Optional custom-field method |
| `RTORRENT_TLS_VERIFY` | `true` | Verify HTTPS certificates |

For HTTP/HTTPS Prowlarr torrent URLs, Librarr fetches and validates the
`.torrent` bytes locally, then submits them with `load.raw_start`. Magnets are
submitted directly with `load.start`. The Prowlarr URL must be configured so
the API key is only sent to the approved Prowlarr origin.

## Remote path mappings

Mappings are scoped to the `rtorrent` client. The resolver normalizes path
separators, enforces prefix boundaries, chooses the longest matching remote
prefix, and rejects paths that escape the local prefix. It does not search the
filesystem for alternatives.

For `/downloads/rclone-mnt/downloads/example.epub`, map remote
`/downloads/rclone-mnt/downloads` to local `/data/incoming` to resolve it as
`/data/incoming/example.epub` inside Librarr.

## Completed-download flow

1. A release is submitted to the selected client.
2. Librarr persists client ID, client type, hash, media type, category, and
   remote save path in `tracked_downloads`.
3. The watcher polls that same client and hash after restart.
4. The reported remote path is resolved with the client-scoped mapping.
5. Missing synchronized content remains pending; it is never guessed by
   scanning unrelated directories.
6. Once present, the configured normalized import engine imports the content.

## Dogfood checklist

1. Find the XML-RPC endpoint used by ruTorrent; do not guess from the web UI URL.
2. Enable rTorrent, enter the endpoint and credentials, and run diagnostics.
3. Configure rTorrent as the active torrent client only after diagnostics pass.
4. Add the mapping `remote=/downloads/rclone-mnt/downloads` to
   `local=/data/incoming`.
5. Submit one small magnet or `.torrent` release and record its hash.
6. Confirm `GET /api/downloads` shows the rTorrent client, hash, status, and
   remote/local path evidence.
7. Allow synchronization to complete and confirm normalized import succeeds.
8. Confirm the torrent remains present and seeding. Do not use cleanup controls;
   removal is not implemented.

To roll back, select qBittorrent as the active client and restart Librarr. This
does not alter existing rTorrent torrents or tracked rows.
