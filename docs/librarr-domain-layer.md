# Librarr Domain Layer

`internal/library` is the Librarr 2.0 domain boundary. It defines how the rest
of the application should think about books, editions, contributors, series,
files, identifiers, covers, matching, import stages, and delivery without
requiring callers to know how those concepts are stored.

This package is intentionally not a production cutover. Existing imports, REST
handlers, OPDS, UI, tags, stats, and download behavior still use
`library_items`.

## Responsibilities

The package owns domain terminology and contracts:

- `Book`, `Edition`, `Contributor`, `Series`, `BookFile`, `Identifier`, and
  `Cover`
- repository interfaces for logical operations
- matching contracts and evidence types
- file service contracts for managed file operations and downloads
- import pipeline stage contracts
- compatibility translation between domain objects and legacy library items

The package should not expose SQL joins, table names, route names, or browser
response details.

## Repository Pattern

Repository interfaces describe application intentions rather than CRUD tables.
For example, future code should ask for `FindBookByIdentifier`,
`GetBookFiles`, or `AttachFile`, not for a specific SQL query against `books`
or `files`.

The first implementation is contract-first. Normalized-schema repositories will
arrive in a later phase after matching and backfill plans are ready.

## Legacy Compatibility

`LegacyLibraryRepository` adapts the current `library_items` storage path into
the new interfaces. It represents each legacy row as one logical book with one
file, matching today's production behavior.

The adapter is read-only for new domain writes. This prevents accidental
cutover while still allowing future API and service code to start depending on
the domain boundary.

Compatibility translators convert:

```text
legacy LibraryItem -> Book + BookFile
Book + BookFile    -> legacy LibraryItem DTO
```

This gives future REST handlers a bridge while preserving current response
compatibility.

## Future Migration Path

The intended sequence is:

1. Keep `library_items` active.
2. Introduce application code against `internal/library` interfaces.
3. Implement normalized repositories behind those interfaces.
4. Backfill normalized tables with conservative matching.
5. Cut over selected reads behind compatibility DTOs.
6. Cut over imports after idempotency and rollback tests pass.
7. Retire legacy storage only after production validation.

## API Cutover

Future API handlers should depend on `internal/library` interfaces rather than
`internal/db` legacy methods. Compatibility DTOs allow existing endpoints to
keep their response shape while the backing repository changes from
`library_items` to normalized `books`, `editions`, and `files`.

New `/api/v1` endpoints can then expose logical books and explicit file
resources directly.
