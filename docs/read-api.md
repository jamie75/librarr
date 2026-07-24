# Librarr 2.0 Read API

Librarr now exposes a native normalized read API under `/api/v1`.

This milestone is intentionally read-only. It does not change import, delete,
send-to-device, metadata refresh, or compatibility write behavior.

## Endpoints

- `GET /api/v1/books`
- `GET /api/v1/books/{id}`
- `GET /api/v1/books/{id}/files`
- `GET /api/v1/books/{id}/editions`

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

The Librarr 2.0 UI path now prefers `/api/v1/books` for ebook cards when the
public config reports `library_repository_mode=normalized`.

Legacy mode continues using `/api/library` compatibility responses.
