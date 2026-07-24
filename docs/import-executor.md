# Import Executor

`internal/library/import.ImportExecutor` is the write-side companion to the
Import Pipeline v2 planner.

Production Librarr still uses the legacy importer. The executor exists so the
normalized import path can be validated behind `LibraryService` before any
download, watcher, manual-import, scanner, or API cutover happens.

## Execution Flow

```text
ImportPlan
  ↓
ImportExecutor
  ↓
LibraryService
  ↓
Normalized repositories
  ↓
books / editions / contributors / files / identifiers / covers
```

For each plan the executor:

1. Re-resolves the target book inside a transaction.
2. Re-resolves the target edition inside the same transaction.
3. Re-checks duplicate path, source ID, content hash, and same-format conflicts.
4. Attaches contributors idempotently.
5. Persists identifiers when present.
6. Attaches the file and saves embedded metadata.

Plans already marked as `ignore_duplicate_file`, `conflict`, or
`needs_manual_review` are not written.

## Transaction Model

- One transaction per `ImportPlan`.
- Batch execution is a sequence of per-plan transactions.
- A failure in one plan does not roll back a previously committed plan.
- A failure inside one plan rolls back every write from that plan.

Transaction ownership remains in `LibraryService` / repository wiring.
`ImportExecutor` only calls `LibraryService.WithinTransaction(...)`.

## Idempotency Strategy

The executor does not trust the planner’s earlier snapshot alone. Before
creating anything it re-checks the normalized model by:

- trusted identifiers
- exact title plus author matching
- exact edition title under the selected book
- file path
- file source ID
- file content hash
- existing formats already attached to the book

This means the same `ImportPlan` can be executed twice and converge on the same
final state without creating duplicate books, editions, contributors, or files.

## Rollback Behavior

If any write fails:

- the transaction is rolled back
- the result status becomes `rolled_back`
- later plans in the batch may still continue

Typical rollback causes include invalid file payloads, conflicting identifiers,
or repository validation failures.

## Future Production Cutover

The intended cutover path is:

1. Keep production imports on the legacy pipeline.
2. Validate planner + executor behavior with focused tests.
3. Add a compatibility execution path behind `LibraryService.ImportCandidate`.
4. Cut over watcher/manual/scanner entry points only after idempotency and
   operational validation are complete.

The executor deliberately has no direct dependency on `sql.DB` or repository
implementations. It writes only through `LibraryService`.
