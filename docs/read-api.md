# Librarr 2.0 Read API

Librarr now exposes a native normalized read API under `/api/v1`.

This milestone is intentionally read-only. It does not change import, delete,
send-to-device, metadata refresh, or compatibility write behavior.

## Endpoints

- `GET /api/v1/books`
- `GET /api/v1/books/{id}`
- `GET /api/v1/books/{id}/files`
- `GET /api/v1/books/{id}/editions`
- `GET /api/v1/books/{id}/cover`
- `GET /api/v1/library/summary`

## Repository mode behavior

These endpoints are native Librarr 2.0 endpoints.

- When `LIBRARR_LIBRARY_REPOSITORY_MODE=normalized`, they return data from the
  normalized `books`, `editions`, `files`, `contributors`, `identifiers`,
  `series`, and `covers` tables through `LibraryService`.
- When `LIBRARR_LIBRARY_REPOSITORY_MODE=legacy`, they return a stable
  service-unavailable JSON error instead of reconstructing books from
  `library_items`.

Existing compatibility endpoints such as `/api/library` remain available and
unchanged.

## Media type support

The normalized read API now supports:

- `ebook`
- `audiobook`
- `manga`

The same `GET /api/v1/books` endpoint handles all three through the
`media_type` query parameter.

## Query parameters

`GET /api/v1/books` supports:

- `limit`
- `offset`
- `search`
- `media_type`
- `format`
- `sort`
- `order`

Supported `sort` values:

- `title`
- `author`
- `recently_added`
- `recently_updated`

Supported `order` values:

- `asc`
- `desc`

## Response model

The list endpoint returns one item per logical book, plus pagination metadata.

Each book summary includes:

- core book metadata
- primary author
- contributor role summaries
- identifiers
- available formats
- edition count
- file count
- cover availability

The detail endpoint includes edition summaries and book-level metadata already
represented in storage. It does not fabricate unsupported fields.

The files endpoint returns all files attached to the logical book across all
editions.

The summary endpoint returns normalized totals directly from the Librarr 2.0
tables, including:

- total logical books
- total editions
- total files
- ebook count
- audiobook count
- manga count
- format distribution
- recently added count

The cover endpoint serves a stored local cover image when one exists. It does
not proxy external images in this milestone.

## Performance strategy

`GET /api/v1/books` now uses a batched repository-backed read model instead of
performing per-book enrichment. The normalized repository loads:

- base books
- contributors and primary authors
- identifiers
- series
- edition/file counts
- format lists
- local primary-cover availability

in bounded query count rather than one query per book.

## Architecture

The read path is:

```text
API handler
  -> LibraryService
  -> repository interfaces
  -> NormalizedRepository
```

Handlers do not query `sql.DB` directly.

## Frontend behavior

When the public config reports `library_repository_mode=normalized`, the
Librarr 2.0 UI now uses `/api/v1/books` for:

- ebooks
- audiobooks
- manga

The Home dashboard uses `/api/v1/library/summary` for totals and format
distribution in normalized mode.

Legacy mode continues using `/api/library` compatibility responses.
