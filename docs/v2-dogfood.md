# Librarr 2.0 Dogfood Deployment

Warning: this is a development-only deployment for local dogfooding. It is
designed to run beside an existing Librarr 1.x instance without sharing state.

## Goals

The dogfood stack is isolated from production:

- separate database and settings storage
- separate organized library paths
- separate incoming/test-download paths
- separate container name
- separate host port
- explicit Librarr 2.0 feature flags

The repository keeps the stable production compose file unchanged. Dogfooding
uses [docker-compose.v2-dogfood.yml](/Users/jamiestephens/Projects/librarr/docker-compose.v2-dogfood.yml).

## Current port layout

The checked-in production compose file maps:

- `5050:5050`

The dogfood stack maps:

- `5051:5050`

## Feature flags

The dogfood deployment enables:

- `LIBRARR_LIBRARY_REPOSITORY_MODE=normalized`
- `LIBRARR_IMPORT_ENGINE=v2`

## Host paths

The dogfood compose file uses repo-local bind mounts under `.dogfood/` so
contributors can inspect and reset state easily:

- `./.dogfood/librarr-v2-data` → `/data`
- `./.dogfood/librarr-v2-library/ebooks` → `/books/ebooks`
- `./.dogfood/librarr-v2-library/audiobooks` → `/books/audiobooks`
- `./.dogfood/librarr-v2-library/manga` → `/books/manga`
- `./.dogfood/librarr-v2-incoming` → `/data/incoming`
- `./.dogfood/librarr-v2-manga-incoming` → `/data/manga-incoming`

This avoids sharing the production database, settings file, or organized
library paths.

## Fresh normalized startup behavior

Normalized startup now distinguishes two cases:

1. fresh normalized install
   - schema migrations complete
   - `library_items` contains zero rows
   - startup is allowed without fabricating a completed backfill

2. migrated legacy install
   - `library_items` contains one or more rows
   - normalized startup still requires the existing backfill and validation
     readiness checks

This keeps migration safeguards intact for real upgrades while allowing a clean
dogfood database to boot.

## Prerequisites

- Docker
- Docker Compose v2
- `curl`
- `python3`

## Build only

```bash
docker compose -f docker-compose.v2-dogfood.yml config -q
docker compose -f docker-compose.v2-dogfood.yml build
```

The built image tag is:

```text
librarr:v2-dogfood
```

It is local-only unless you explicitly retag and push it.

## Start

```bash
mkdir -p .dogfood/librarr-v2-data \
         .dogfood/librarr-v2-library/ebooks \
         .dogfood/librarr-v2-library/audiobooks \
         .dogfood/librarr-v2-library/manga \
         .dogfood/librarr-v2-incoming \
         .dogfood/librarr-v2-manga-incoming

docker compose -f docker-compose.v2-dogfood.yml up -d
```

Open:

```text
http://127.0.0.1:5051
```

## Stop

```bash
docker compose -f docker-compose.v2-dogfood.yml down --remove-orphans
```

## Reset / remove all dogfood state

```bash
docker compose -f docker-compose.v2-dogfood.yml down --remove-orphans
rm -rf .dogfood/librarr-v2-data \
       .dogfood/librarr-v2-library \
       .dogfood/librarr-v2-incoming \
       .dogfood/librarr-v2-manga-incoming
```

## Contributor-friendly smoke test

A reproducible smoke script is included:

- [scripts/v2-dogfood-smoke.sh](/Users/jamiestephens/Projects/librarr/scripts/v2-dogfood-smoke.sh)

It performs:

1. clean reset of dogfood state
2. compose validation
3. image build
4. isolated startup
5. health wait
6. first-admin registration
7. normalized/v2 mode verification
8. EPUB import
9. MOBI import for the same logical book
10. logical-book + formats verification
11. manual metadata override
12. container restart
13. post-restart persistence verification

Run it with:

```bash
./scripts/v2-dogfood-smoke.sh
```

## Manual smoke checklist

If you want to walk the flow manually in the UI:

1. Build and start the dogfood stack.
2. Open `http://127.0.0.1:5051`.
3. Register the first user; it becomes admin automatically.
4. Confirm `/api/config` reports:
   - `library_repository_mode=normalized`
   - `import_engine=v2`
5. Place one EPUB and one MOBI test file under `.dogfood/librarr-v2-incoming`.
6. Use manual import to import both with the same logical title/author.
7. Verify one logical book appears with two formats.
8. Edit one metadata field in Book Details.
9. Restart the container.
10. Verify the metadata override and provenance still exist.

## Health check

The compose file uses:

```text
GET /api/health
```

against container port `5050`.

## Known limitations

- This is not a production deployment.
- The dogfood stack intentionally does not inherit your production `.env`.
- The smoke script uses placeholder `.epub` and `.mobi` files to exercise the
  import pipeline, not full real-world metadata extraction.
- qBittorrent, Prowlarr, and other external integrations are not configured by
  default in the dogfood stack.
- If a contributor wants to test against a copied real library, they should use
  a copied or read-only source, not the production write destination.
