# Library Scanner Phase 1

Phase 1 adds the backend scanner and review payload for importing an existing
library into Librarr 2.0. It does not import files, move files, create books, or
write to `library_items`, `books`, `editions`, `files`, or related normalized
tables.

## Architecture

The scan API is intentionally thin:

```text
API
  -> scanner.Manager
  -> filesystem walk of configured library roots
  -> ImportPlanner metadata and duplicate decisions
  -> in-memory review payload
```

The scanner reads the current Library & Import folder configuration when a scan
starts. Saved `settings.json` values override startup config defaults so a user
can save folders and scan without restarting the container.

## Endpoints

`POST /api/v1/library/scan`

Starts a scan job and returns `202 Accepted` with a job id. Only one scan can run
at a time; a second start request receives `409 Conflict` with the active job id.

`GET /api/v1/library/scan/{job_id}`

Returns job status and progress.

`GET /api/v1/library/scan/{job_id}/results`

Returns the completed review payload. If the job is not complete, the endpoint
returns `409 Conflict`.

The endpoints require an authenticated administrator session or API key because
they expose configured application file paths.

## Supported Formats

Ebook root:

- `.epub`
- `.pdf`
- `.mobi`
- `.azw3`

Audiobook root:

- `.m4b`
- `.mp3`

Manga root:

- `.cbz`
- `.cbr`
- `.pdf`

Extension matching is case-insensitive. PDF is classified by the configured root:
ebook PDFs under the ebook root, manga PDFs under the manga root.

## Candidate Schema

Each result candidate includes:

- stable candidate id for the scan result
- media type
- format
- absolute application/container path
- relative path within the configured root
- filename
- file size
- modified time
- title and author
- series and volume placeholders
- metadata source and confidence
- classification and reason
- error details for unreadable files

Metadata uses existing embedded extraction where available and falls back to the
existing filename parser when embedded metadata is missing or unsupported.

## Classification Rules

`new`

The planner found no existing file conflict. The file is ready for a future
review/import step.

`already_imported`

The exact file path already exists in the selected library repository. This means
the scanner found the same application path that Librarr already tracks.

`duplicate`

The file appears to duplicate an existing library file without being the exact
same tracked path. Phase 1 detects duplicate content hashes and planner conflicts
such as an existing format for the resolved book.

`unsupported`

The file extension is not supported for the configured media root.

`unreadable`

The scanner could not stat or read the file. The scan continues.

## Job Lifecycle

Jobs are in memory only. Completed jobs/results are retained using a small
bounded retention policy. This is enough for the Step 2 review flow without
adding Redis or another external dependency.

Statuses:

- `pending`
- `scanning`
- `processing_metadata`
- `classifying`
- `completed`
- `failed`
- `cancelled`

Progress includes the current media type, current path/phase, directories
scanned, files discovered, files processed, candidates ready, warnings, and
timestamps.

## Missing Folders

A missing or inaccessible configured folder becomes a folder-level warning. The
scanner continues with the remaining configured roots.

## Current Limitations

- No Import Selected, Import All, or Skip Selected persistence.
- No frontend review UI in Phase 1.
- No file organization or moves.
- No external metadata providers.
- Duplicate detection intentionally reuses the current planner/service path; a
  later performance pass can add repository-level batch lookups if large real
  libraries need more optimization.
