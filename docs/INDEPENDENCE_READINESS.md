# Librarr Independence Readiness

This document records the current state of the repository as Librarr prepares to
become an independently maintained project. It does not detach the GitHub fork,
delete history, tag a release, or publish a container image.

## Origin and attribution

Librarr began as a fork of the original Librarr project by Jeremiah Mackey. The
repository license is MIT and the original copyright notice remains in
[LICENSE](../LICENSE).

[ACKNOWLEDGMENTS.md](../ACKNOWLEDGMENTS.md) documents origin and attribution in
plain language without implying endorsement by the original author.

## Current divergence

The `feature/librarr-2` branch has substantially diverged through:

- normalized book-centric schema and repository selection
- LibraryService and repository switch
- migration/backfill framework
- import planner, executor, and feature-flagged import engine
- redesigned first-run onboarding
- scan, review, manual resolution, and explicit import workflow
- metadata editing
- embedded EPUB cover extraction and local cover caching
- expanded user management
- staged diagnostics for Prowlarr and qBittorrent
- remote qBittorrent torrent upload and path-mapping fixes
- OPDS 1.2 catalog with Basic Auth and safe file downloads
- independent documentation, architecture, and roadmap

## Repository identity audit

Observed local repository state:

- current branch: `feature/librarr-2`
- `origin`: `https://github.com/jamie75/librarr.git`
- `upstream`: `https://github.com/jeremiahm37/librarr.git`
- default remote HEAD: `origin/main`
- local active branches: `main`, `feature/librarr-2`,
  `fix/qbittorrent-downloads`
- known v2 milestone tags:
  - `v2.0-architecture-complete`
  - `v2.0-core-complete`
  - `v2.0-dogfood-ready`
  - `v2.0-import-engine`
  - `v2.0-repository-switch`

Missing or not present in the local tree:

- issue templates
- pull request template
- CODEOWNERS
- SECURITY.md
- NOTICE file

Those are not blockers for development, but they should be added before a
public independence announcement.

## Active upstream dependencies and references

Valid attribution references may remain in:

- [LICENSE](../LICENSE)
- [ACKNOWLEDGMENTS.md](../ACKNOWLEDGMENTS.md)
- this document

The built-in sources registry still defaults to:

```text
https://raw.githubusercontent.com/JeremiahM37/librarr-sources/main/sources.json
```

That is an operational dependency on a companion repository outside this code
base. Do not change it blindly unless a maintained Jamie-owned registry exists
and has been validated. Recommended follow-up: create or confirm
`jamie75/librarr-sources`, mirror the registry, then update the default URL or
document `LIBRARR_SOURCES_URL` as the supported override.

## Module and import-path status

The Go module path has been migrated to:

```text
github.com/jamie75/librarr
```

Internal imports, tests, GoReleaser ldflags, and the OpenLibrary User-Agent were
updated to the same namespace.

## Docker and release identity

The intended independent image is:

```text
ghcr.io/jamie75/librarr
```

Recommended release tags:

- `v2-dogfood` for current validation builds
- `v2.0.0-beta.1` for first public beta
- `v2.0.0` for first stable release
- `latest` only for stable releases

The release workflow now publishes `latest` only for stable semver tags matching
`vMAJOR.MINOR.PATCH`.

## Branch strategy

`feature/librarr-2` is 48 commits ahead of `main` and not behind it in the local
audit. Recommended sequence:

1. Finish validation on `feature/librarr-2`.
2. Open a PR from `feature/librarr-2` into `main`.
3. Merge into `main` when ready.
4. Treat `main` as the release-ready branch.
5. Retire `feature/librarr-2` after the 2.0 line is established.
6. Use short-lived feature branches from `main` for future work.

Do not keep `feature/librarr-2` as a permanent development trunk after
independence.

## GitHub fork detachment checklist

Official GitHub documentation describes detaching a fork from repository
settings by using **Settings → General → Danger Zone → Leave fork network** and
confirming the effects before leaving the fork network:

```text
https://docs.github.com/en/pull-requests/how-tos/work-with-forks/detaching-a-fork
```

GitHub's documented built-in detachment path currently requires the fork to be
public, under 1 GB, and without child forks. GitHub also warns that leaving the
fork network does not retain issues, pull requests, wikis, stars, watchers,
comments, child forks, or other fork-associated metadata, while Git commit
metadata is preserved.

Before using that destructive repository action:

- [ ] Merge or intentionally close all open PRs targeting the fork.
- [ ] Export or preserve issues, PRs, discussions, labels, projects, and wiki
      content as needed.
- [ ] Check whether any child forks depend on the current fork network.
- [ ] Confirm repository size is under GitHub's current detachment limit.
- [ ] Create a fresh backup clone with all branches and tags.
- [ ] Verify `main` is clean and release-ready.
- [ ] Verify branch protection and required checks.
- [ ] Verify GitHub Actions permissions and package publishing.
- [ ] Verify `ghcr.io/jamie75/librarr` package visibility and pull access.
- [ ] Verify badges and documentation links after detachment.
- [ ] Verify package/module paths are final.
- [ ] Publish release notes explaining attribution and independence.
- [ ] Review GitHub's current warnings in the detachment dialog immediately
      before executing.

Do not assume GitHub preserves every relationship or metadata item exactly as
desired; review the live warning text before final action.

## Release readiness

Completed foundation:

- onboarding
- normalized repository
- scanner
- review
- explicit import
- manual review
- metadata editing
- local EPUB covers
- users
- diagnostics
- qBittorrent remote path support
- OPDS 1.2

Known release gaps:

- add SECURITY.md
- add issue and PR templates
- decide or migrate the `librarr-sources` registry
- verify package visibility and GHCR pull behavior
- perform fresh-install and upgrade smoke tests from Docker images
- polish remaining Library metadata edit behavior
- document supported OPDS clients after real-device testing
- decide whether to add a NOTICE file in addition to ACKNOWLEDGMENTS

## Recommended final sequence

1. Merge `feature/librarr-2` into `main`.
2. Run full local and CI validation.
3. Publish a dogfood image under `ghcr.io/jamie75/librarr:v2-dogfood`.
4. Validate real upgrade and fresh-install flows.
5. Resolve the sources-registry dependency.
6. Add public repo hygiene files: SECURITY.md, issue templates, PR template,
   and CODEOWNERS if desired.
7. Tag `v2.0.0-beta.1`.
8. Publish beta release notes.
9. Only after beta validation, decide whether to leave the GitHub fork network.

## Go/no-go recommendation

Go for continued Librarr 2.0 dogfood and beta preparation.

No-go for immediate fork detachment until the sources-registry dependency,
repository hygiene files, release smoke tests, GHCR visibility, and GitHub
detachment consequences are reviewed one final time.
