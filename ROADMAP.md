# Librarr Roadmap

## Purpose

This roadmap turns the Librarr vision and architecture into staged, testable milestones.

The order matters. Librarr should establish a durable book-centric foundation before expanding into broad automation, delivery, and Media Assistant integration.

The roadmap is intentionally practical. Each milestone should leave the application usable, deployable, and testable.

## Current Baseline

The current codebase already provides a useful foundation, including:

- Docker deployment
- SQLite storage
- web UI and authentication
- Prowlarr/indexer integration
- torrent download workflows
- qBittorrent and Transmission support
- completed-download monitoring
- file organization
- EPUB embedded metadata extraction
- MOBI filename fallback handling
- content-hash duplicate prevention
- OPDS support
- REST endpoints

The immediate architectural limitation is that `library_items` represents both a logical book and a physical file.

## Milestone 2.0 — Book-Centric Foundation

### Objective

Replace the file-centric library core with a normalized domain model while preserving current import and deployment stability.

### 2.0.1 Architecture and migration fixtures

- [x] Define product vision.
- [x] Define target architecture.
- [x] Define staged roadmap.
- [ ] Document the current `library_items` schema and every read/write path.
- [ ] Add fixtures for:
  - same book in EPUB and MOBI
  - same title by different authors
  - EPUB and PDF editions
  - missing author
  - manga CBZ and CBR
  - duplicate content under different paths
  - conflicting embedded metadata
- [ ] Add migration test harness using a copied test database.
- [ ] Define rollback and validation procedure.

### 2.0.2 Normalized schema

- [x] Add domain tables additively:
  - `books`
  - `editions`
  - `contributors`
  - `edition_contributors`
  - `series`
  - `files`
  - `identifiers`
  - `covers`
- [x] Add indexes for normalized names, foreign keys, hashes, formats, and source IDs.
- [x] Preserve `library_items` during the transition.
- [x] Add explicit schema versioning and migration status logging.

### 2.0.3 Conservative matching service

- [ ] Introduce a dedicated book/edition matching service.
- [ ] Match trusted identifiers before text heuristics.
- [ ] Use normalized title, primary author, media type, series, and publication data as fallback evidence.
- [ ] Return match confidence and evidence.
- [ ] Keep ambiguous candidates separate.
- [ ] Add user-confirmed merge and split design, even if the UI arrives later.

### 2.0.4 Backfill existing library

- [ ] Backfill normalized records from `library_items`.
- [ ] Preserve legacy rows and migration linkage.
- [ ] Produce validation totals for books, editions, and files.
- [ ] Report ambiguous records instead of silently merging them.
- [ ] Verify that every old managed path maps to exactly one new file row.

### 2.0.5 Repository and import cutover

- [x] Add repositories for books, editions, contributors, series, and files.
- [x] Build the planner/executor import path behind a feature flag.
- [x] Update completed torrent imports, direct downloads, and manual import to support the normalized engine.
- [x] Preserve file-level path/hash duplicate prevention.
- [x] Make repeated completed-download events idempotent.
- [x] Coordinate filesystem and database failure recovery.
- [x] Add rollback-by-configuration through `LIBRARR_IMPORT_ENGINE=legacy`.
- [ ] Switch new writes to the normalized model.

### 2.0.6 Compatibility API

- [ ] Add `/api/v1/books` and explicit file resources.
- [ ] Keep existing library endpoints available during transition.
- [ ] Return one logical book with a `formats` list and `files` collection.
- [ ] Preserve a preferred/default file in compatibility responses.
- [ ] Add `total_books`, `total_files`, and format counts to stats.
- [ ] Document transitional fields.

### 2.0.7 Library UI grouping

- [ ] Display one card or row per logical book.
- [ ] Show available formats without duplicate-looking entries.
- [ ] Preserve ebook, audiobook, manga, and comic filtering.
- [ ] Add clear empty, ambiguous, and import-error states.
- [ ] Defer complex editing until the normalized model is stable.

### 2.0 exit criteria

- One logical book can own multiple physical formats.
- Existing library data migrates without lost files.
- Current qBittorrent import behavior remains functional.
- Repeated imports do not create duplicate files or books.
- Compatibility APIs remain usable.
- The Docker image builds and deploys on amd64 and arm64.
- Migration and repository tests pass in CI.

## Milestone 2.1 — Download Client Framework

### Objective

Provide *arr-style configurable download clients through a common interface.

### Core framework

- [ ] Define internal download client capabilities.
- [ ] Store multiple configured clients.
- [ ] Support enable/disable, priority, categories, and optional save paths.
- [ ] Add connection testing and health reporting.
- [ ] Mask credentials in API responses and logs.
- [ ] Separate torrent and Usenet capabilities where behavior differs.

### Client adapters

- [ ] Treat qBittorrent as the reference torrent implementation.
- [ ] Bring Transmission into the same adapter interface.
- [ ] Add rTorrent support.
- [ ] Add Deluge support.
- [ ] Validate completed-download handling against each client.
- [ ] Add client-specific diagnostics without leaking implementation details into the import pipeline.

### Settings UI

- [ ] Add a familiar **Settings → Download Clients** page.
- [ ] Support add, edit, delete, enable, disable, test, and priority.
- [ ] Display implementation-specific fields only when relevant.
- [ ] Add a completed-download handling section.

### 2.1 exit criteria

- qBittorrent, Transmission, rTorrent, and Deluge use one stable application-facing contract.
- The user can configure and test clients from the UI.
- Import code does not switch on client type outside the adapter layer.

## Milestone 2.2 — Metadata and Cover System

### Objective

Make metadata dependable, explainable, editable, and reusable across file formats.

### Metadata providers

- [ ] Define metadata provider interface.
- [ ] Evaluate initial providers such as Open Library and Google Books.
- [ ] Store provider identifiers and provenance.
- [ ] Score candidate matches.
- [ ] Avoid destructive refresh of user overrides.
- [ ] Add rate limiting, caching, and provider health reporting.

### Embedded metadata

- [ ] Preserve current EPUB extraction behavior.
- [ ] Preserve MOBI filename fallback as a last-resort source.
- [ ] Store embedded metadata at the file level.
- [ ] Resolve display metadata through explicit precedence rules.
- [ ] Add format-specific extractor tests.

### Covers

- [ ] Extract covers from supported local formats where practical.
- [ ] Download covers from selected metadata providers.
- [ ] Cache covers locally.
- [ ] Store cover provenance, dimensions, and checksum.
- [ ] Serve covers from Librarr-owned API URLs.
- [ ] Provide placeholders only when no cover is available.

### Editing and refresh

- [ ] Add book/edition metadata edit APIs.
- [ ] Add refresh preview before applying provider changes.
- [ ] Add field-level user overrides.
- [ ] Record metadata history for troubleshooting.

### 2.2 exit criteria

- Metadata is stored once per logical edition, not duplicated per format.
- Users can understand where key fields came from.
- Covers persist locally and survive provider outages.

## Milestone 2.3 — *arr Media Management Experience

### Objective

Make Librarr operationally familiar to Sonarr and Radarr users.

### Media Management

- [ ] Root folder management.
- [ ] Naming templates with previews.
- [ ] Rename existing files safely.
- [ ] Copy, move, and hardlink policies.
- [ ] Rescan and missing-file detection.
- [ ] Manual import workflow.

### Profiles

- [ ] Quality profiles for ebook, audiobook, manga, and comic formats.
- [ ] Preferred and cutoff format behavior.
- [ ] Release profiles with preferred and excluded terms.
- [ ] Upgrade policy that does not unnecessarily remove useful formats.

### Operations

- [ ] Activity queue.
- [ ] History.
- [ ] Blocklist.
- [ ] Health checks.
- [ ] System status.
- [ ] Backup and restore validation.
- [ ] Log level controls and support bundle.

### 2.3 exit criteria

- A user familiar with another *arr can configure core workflows without learning unique terminology.
- File organization, upgrades, and failures are visible and recoverable.

## Milestone 2.4 — Monitoring and Automated Acquisition

### Objective

Move from one-off search/download behavior to reliable library automation.

- [ ] Monitor individual books.
- [ ] Monitor editions.
- [ ] Monitor contributors/authors.
- [ ] Monitor series and identify gaps.
- [ ] Schedule RSS or periodic indexer checks.
- [ ] Apply quality and release profiles automatically.
- [ ] Record grab decisions and rejection reasons.
- [ ] Prevent repeated grabs of blocked or failed releases.
- [ ] Add manual and automatic search parity.

### 2.4 exit criteria

- Librarr can automatically acquire a monitored title according to configured rules.
- Every automatic decision is visible in history and explainable.

## Milestone 2.5 — OPDS and Delivery

### Objective

Make acquired books easy to consume.

### Downloads and OPDS

- [ ] Download a specific file by file ID.
- [ ] Download a book using a requested or preferred format.
- [ ] Expose one OPDS entry per logical book.
- [ ] Include multiple acquisition links for available formats.
- [ ] Preserve OPDS 1.2 compatibility where needed.
- [ ] Add OPDS 2.0 support.

### Device destinations

- [ ] Define device/destination model.
- [ ] Add preferred formats and size limits.
- [ ] Add direct download destination.
- [ ] Add watched-folder delivery.
- [ ] Add Kindle email delivery.
- [ ] Record delivery history and failures.
- [ ] Evaluate Kobo and Apple Books workflows.

### Optional conversion integration

- [ ] Evaluate Calibre integration for explicit conversion requests.
- [ ] Keep conversion optional and outside the core import requirement.
- [ ] Never perform silent lossy conversion.

### 2.5 exit criteria

- A user can choose a book and acquire or send an appropriate available format.
- Clients do not need to understand Librarr's internal storage paths.

## Milestone 2.6 — Media Assistant Integration

### Objective

Expose Librarr as the authoritative book service for Media Assistant.

### API capabilities

- [ ] Search books, contributors, and series.
- [ ] Determine ownership and available formats.
- [ ] Add a wanted or monitored title.
- [ ] Trigger search or acquisition.
- [ ] Retrieve recent additions and activity.
- [ ] Trigger device delivery.
- [ ] Expose stable event/webhook payloads.

### Integration principles

- [ ] Media Assistant uses the public API only.
- [ ] Librarr remains fully usable without Media Assistant.
- [ ] Cross-media recommendations and household presentation remain Media Assistant responsibilities.
- [ ] Book metadata, files, acquisition, OPDS, and delivery remain Librarr responsibilities.

### 2.6 exit criteria

- Media Assistant can discover, request, inspect, and deliver books through documented Librarr APIs.
- No direct database coupling exists between the projects.

## Milestone 3.0 — Mature Personal Library Platform

Potential scope after the 2.x foundation is proven:

- multiple users and per-user reading state
- reading progress integrations
- richer audiobook chapter and track modeling
- edition comparison and manual merge/split UI
- advanced duplicate review
- recommendation signals
- event subscriptions
- mobile-friendly or native clients
- broader provider ecosystem
- localization and accessibility improvements

These items should not delay the book-centric foundation.

## Engineering Workstreams

The following apply across all milestones.

### Testing

- migration tests
- repository tests
- import idempotency tests
- client adapter contract tests
- API compatibility tests
- filesystem failure tests
- end-to-end Docker smoke tests

### Documentation

- architecture decision records for major choices
- OpenAPI documentation
- migration and rollback instructions
- Docker deployment examples
- supported-client capability matrix
- troubleshooting guides

### CI/CD

- Go formatting and tests
- static analysis
- amd64 and arm64 image builds
- migration smoke tests
- release notes and schema version reporting

### Security

- secret masking
- path traversal protection
- authentication and API key review
- dependency updates
- parser hardening for untrusted files
- backup/restore validation

## Near-Term Recommended Task Order

The next implementation sequence should be:

1. Inventory every current `library_items` read and write path.
2. Add representative multi-format and ambiguity fixtures.
3. Decide whether `editions` ships in the first migration or is staged behind one edition per book.
4. Write the additive schema migration.
5. Build backfill and validation tooling.
6. Add normalized repositories.
7. Update the import pipeline.
8. Add compatibility reads and `/api/v1/books`.
9. Update the library UI to show one book with many formats.
10. Begin the download-client adapter framework and add rTorrent.

## Definition of Done for Major Changes

A major change is not complete until:

- code is formatted
- tests pass
- migrations have upgrade and rollback guidance
- Docker builds succeed
- no secrets are exposed in logs or APIs
- user-visible behavior is documented
- the change is deployed to a test instance
- representative real-world imports are verified
