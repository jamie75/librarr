# Apple Books export

Librarr can make a safe, local handoff copy of a normalized audiobook or ebook
for the Apple Books app on macOS. This is an export workflow, not a sync
service: the source file is never moved, transcoded, rewritten, or deleted.

## Configuration

Set these environment variables or use Settings → Apple Books:

```text
APPLE_BOOKS_EXPORT_ENABLED=false
APPLE_BOOKS_EXPORT_DIR=/data/apple-books-export
APPLE_BOOKS_EXPORT_OVERWRITE=false
```

Mount a host folder such as `/share/Books/Librarr Apple Books Export` at
`/data/apple-books-export`. Librarr stores only a path relative to that root
in API responses; host paths are not exposed.

Export is opt-in. Audiobook sources must be beneath the configured audiobook
root, ebook sources beneath the configured ebook root, and outputs beneath the
export root. Traversal and symlink escapes are rejected.

## Formats

- A single M4B is copied as `Author - Title.m4b`.
- A single MP3 is copied as `Author - Title.mp3`.
- Multiple MP3 tracks are copied into an `Author - Title/` package in sorted
  order, with a manifest and any existing local cover.
- A single EPUB is copied as `Author - Title.epub`.
- A single PDF is copied as `Author - Title.pdf`.
- When the author is unknown, the name is `Title.ext` without an `Unknown -`
  prefix.

No conversion occurs. MOBI and AZW3 are not supported in this phase. Completed
and failed exports are retained in the `apple_books_exports` table with media
type, status, format, source counts, destination name, checksum, timestamps,
and a bounded error message.

## macOS handoff

Run `scripts/import-to-books.sh` on the Mac that has the export folder mounted.
It recognizes `.m4b`, `.mp3`, `.epub`, and `.pdf` handoff files.
Set `APPLE_BOOKS_EXPORT_DIR` and optionally `APPLE_BOOKS_ARCHIVE_DIR`. The
script calls `open -a Books` and archives the handoff after that command
succeeds, preventing repeated offers. A successful `open` means the file was
handed to Books; verify it in Books because macOS does not provide a reliable
command-line import acknowledgement.

For polling, copy `docs/com.apple.librarr.apple-books-import.plist.template`,
replace its placeholders, install it under `~/Library/LaunchAgents/`, and load
it with `launchctl`. Keep usernames, tokens, and private environment paths out
of the repository.

## API

- `POST /api/v1/books/{id}/apple-books/export` with `{ "format": "auto" }`
- `GET /api/v1/books/{id}/apple-books/exports`
- `GET /api/v1/apple-books/exports`
- `GET /api/v1/apple-books/exports/{export_id}`

The export endpoint is administrator-only. `auto` selects the source EPUB or
PDF for ebooks; an ebook can also request its available `epub` or `pdf`
format. History endpoints require an
authenticated session or API key. No download client or metadata provider is
involved.

Apple Books or iCloud may copy the handoff into its own managed storage after
the macOS helper opens it. Librarr does not observe or guarantee that final
step; the original Librarr source remains untouched.
