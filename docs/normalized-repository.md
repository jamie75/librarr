# NormalizedRepository

`internal/library.NormalizedRepository` is the future Librarr 2.0 storage
engine for the normalized book-centric schema.

It is implemented but intentionally not wired into production. The running
application still uses `LegacyLibraryRepository` and still reads active library
data from `library_items`.

## Responsibilities

The repository implements the library domain repository interfaces for:

- books
- editions
- files
- contributors and edition contributor roles
- series and book-series positions
- identifiers
- covers
- transaction boundaries

Callers should depend on domain methods such as `FindBook`, `AttachFile`,
`ListBookEditions`, or `PrimaryCover` instead of knowing SQL table names or
join details.

## Table Ownership

`NormalizedRepository` owns access to these normalized tables:

- `books`
- `editions`
- `contributors`
- `edition_contributors`
- `series`
- `book_series`
- `files`
- `identifiers`
- `covers`

It does not read or write `library_items`, and it does not populate
`library_item_migration_map`.

## Transaction Model

The repository implements `TransactionManager` with `WithinTransaction`.

Repository methods automatically use the transaction stored on the context
when called inside `WithinTransaction`; otherwise they execute directly against
the configured `*sql.DB`.

This lets future import and backfill code coordinate multi-table writes without
making every caller pass SQL transaction objects around.

## Expected Invariants

- A book is the logical entity.
- An edition belongs to exactly one book.
- A file belongs to exactly one edition.
- A book can have multiple editions.
- An edition can have multiple physical files and formats.
- Contributors are merge-safe by case-insensitive name/sort-name lookup.
- Contributor roles live on `edition_contributors`.
- Series membership lives on `book_series`.
- Identifiers are unique per owner, provider, and identifier value.
- File paths are unique when populated.
- Primary covers are unique per book or edition.

## Relationship Handling

Book reads hydrate direct book-level contributors, series, identifiers, and
covers where practical without requiring API callers to perform their own SQL
joins.

File reads join through `editions` to expose the owning `BookID`. This keeps
future delivery features, OPDS downloads, Kindle export, and send-to-device
flows able to reason about both the physical file and its logical book.

Series and contributor APIs use upserts or merge-safe lookups where the schema
supports it, while preserving conservative identity rules.

## Metadata Limitation

The current normalized schema does not yet include generic metadata JSON
columns for embedded, provider, or user override metadata. The
`MetadataRepository` methods therefore return `ErrUnsupportedOperation` rather
than inventing storage or changing the schema during this repository-only step.

Future metadata migrations should add explicit provenance-aware storage before
these methods are used in production.

## Repository Factory

`NewRepository(NormalizedRepositoryMode, *sql.DB)` returns a
`NormalizedRepository`.

`NewRepository(LegacyRepositoryMode, *sql.DB)` returns a
`LegacyLibraryRepository` backed by a small SQL legacy store. Production
currently continues to use the established `NewLegacyLibraryRepository` wiring
with `internal/db.DB`; the factory exists so future service wiring can choose a
mode explicitly.

## Future Migration Strategy

1. Keep production reads and writes on `library_items`.
2. Use `NormalizedRepository` in isolated migration and backfill tests.
3. Backfill normalized rows with conservative matching.
4. Validate counts and relationship integrity against legacy data.
5. Switch `LibraryService` wiring behind compatibility DTOs.
6. Cut over imports only after repeated-download idempotency, rollback, and
   filesystem recovery tests pass.
