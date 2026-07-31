# Audiobook metadata

Librarr treats an audiobook as one logical book and edition. A directory of
MP3 tracks is therefore planned as one candidate, while each physical track is
attached as a separate normalized file. A single M4B is attached as one M4B
file.

## Metadata sources

The importer reads local embedded metadata only:

- MP3 ID3v2 title, album, artist/album artist, composer, date, genre, comment,
  track/disc numbers, duration estimate, and APIC artwork.
- M4B/MP4 common title, album, artist, date, description, and artwork atoms.
- The existing path and filename fallback when embedded metadata is absent.

Embedded audiobook metadata is recorded in the normalized file's existing
`embedded_metadata_json` field. This keeps the migration additive and avoids a
second audiobook-specific catalog. The Library API exposes an audiobook
summary with narrator, duration, track count, chapter count, abridged flag,
and formats when those values are available.

## Grouping and review

Files under one audiobook directory are grouped before planning. Consistent
album and artist tags are preferred. Ambiguous or conflicting metadata should
remain in the existing Scan → Review → Import workflow for manual review.
Unknown files are never renamed or rewritten by metadata extraction.

## Covers and limitations

APIC and supported M4B artwork are copied to the existing CoverCache during
import when a book has no valid cover. User-provided covers are not replaced.
No online metadata provider, cover download, embedded-tag writer, or audio
decoder is used. Duration is best-effort for local MP3/M4B metadata; files
that cannot be parsed continue to use filename/path fallback and may require
manual review.
