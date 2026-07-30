# rTorrent / ruTorrent Phase 1

Librarr Phase 1 supports read-only rTorrent inspection through rTorrent's
XML-RPC endpoint. It does not scrape ruTorrent and it does not add, stop,
delete, or import torrents.

## Configuration

Configure `RTORRENT_URL` with the XML-RPC endpoint already exposed by the
seedbox. Keep credentials in the separate fields; credential-bearing URLs are
rejected. The Settings page also supports saving and testing the connection.

| Variable | Default | Purpose |
|---|---:|---|
| `RTORRENT_ENABLED` | `false` | Enables read-only inspection |
| `RTORRENT_NAME` | `rTorrent` | Display name |
| `RTORRENT_URL` | | HTTP/HTTPS XML-RPC endpoint |
| `RTORRENT_USER` | | Optional HTTP basic-auth username |
| `RTORRENT_PASS` | | Optional HTTP basic-auth password |
| `RTORRENT_TIMEOUT_SECONDS` | `10` | RPC request timeout |
| `RTORRENT_LABEL_FIELD` | `d.custom1=` | Optional custom-field method |
| `RTORRENT_TLS_VERIFY` | `true` | Verify HTTPS certificates |

## Remote path mappings

Mappings are scoped to the `rtorrent` client. The resolver normalizes path
separators, enforces prefix boundaries, chooses the longest matching remote
prefix, and rejects paths that escape the local prefix. It does not search the
filesystem for alternatives.

For `/downloads/rclone-mnt/downloads/example.epub`, map remote
`/downloads/rclone-mnt/downloads` to local `/data/incoming` to resolve it as
`/data/incoming/example.epub` inside Librarr.

## Dogfood checklist

1. Find the XML-RPC endpoint used by ruTorrent; do not guess from the web UI URL.
2. Enter it in Settings and run rTorrent diagnostics.
3. Confirm version and XML-RPC success without credentials appearing in responses or logs.
4. Call `GET /api/rtorrent/downloads` as an administrator and verify a known
   download's hash, name, status, progress, size, and content path.
5. Add the remote/local mapping and verify the path preview.
6. Confirm no torrent is submitted, stopped, deleted, or imported by this phase.
