# LibraryService

`internal/library.LibraryService` is the application-facing boundary for
library operations during the Librarr 2.0 transition.

The service exists so HTTP handlers, OPDS, background work, and future API
resources can depend on book/file domain operations instead of reaching
directly into the legacy `internal/db` package. This is an architectural seam,
not a production data cutover.

## Responsibilities

`LibraryService` owns the repository and service dependencies needed for
library operations:

- books
- files
- metadata
- series
- contributors
- identifiers
- covers
- matching
- managed file operations

The first implementation delegates to repositories and preserves existing
behavior. It does not backfill normalized tables, perform new matching, or
change import semantics.

## Current Dependency Flow

```text
HTTP / OPDS / background callers
  ↓
LibraryService
  ↓
LegacyLibraryRepository
  ↓
internal/db
  ↓
library_items
```

`library_items` remains the active production source of truth. Existing
compatibility endpoints still serialize legacy library item DTOs, but selected
callers now obtain those rows through `LibraryService`.

## Repository Interaction

The service is constructed with explicit dependencies. It does not use globals.
Callers can inject test doubles, the legacy repository, or future normalized
repositories.

The legacy repository implements the domain repository interfaces by mapping
each legacy row to one logical book and one file. Domain write operations that
would imply normalized storage remain read-only or unsupported unless they map
to an existing legacy compatibility operation.

## Error Boundary

`LibraryService` translates repository errors into domain errors such as:

- `ErrBookNotFound`
- `ErrInvalidIdentifier`
- `ErrRepositoryReadOnly`
- `ErrUnsupportedOperation`

This prevents future handlers from depending on SQL-specific error text.
Legacy compatibility routes still preserve their existing response shapes.

## Future NormalizedRepository Swap

The intended migration path is:

1. Continue reading and writing `library_items`.
2. Move callers behind `LibraryService` where the operation maps cleanly.
3. Add normalized repositories that implement the same interfaces.
4. Backfill normalized rows from legacy data.
5. Switch service wiring from `LegacyLibraryRepository` to normalized
   repositories behind compatibility DTOs.
6. Cut over imports and new `/api/v1` book/file endpoints after validation.

Because callers depend on `LibraryService`, the repository swap should be a
wiring change plus compatibility validation rather than a broad endpoint
rewrite.
