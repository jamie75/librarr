#!/usr/bin/env bash
set -euo pipefail

# Handoff helper for macOS. Configure paths in the environment.
EXPORT_DIR="${APPLE_BOOKS_EXPORT_DIR:-/Volumes/Librarr Apple Books Export}"
ARCHIVE_DIR="${APPLE_BOOKS_ARCHIVE_DIR:-${EXPORT_DIR%/}/archive}"
BOOKS_APP="${BOOKS_APP:-Books}"

mkdir -p "$ARCHIVE_DIR"
find "$EXPORT_DIR" -maxdepth 1 -type f \( -iname '*.m4b' -o -iname '*.mp3' -o -iname '*.epub' -o -iname '*.pdf' \) -print0 |
while IFS= read -r -d '' file; do
  if open -a "$BOOKS_APP" "$file"; then
    mv -- "$file" "$ARCHIVE_DIR/"
    printf 'Handed off and archived: %s\n' "$(basename "$file")"
  else
    printf 'Books handoff failed; leaving in place: %s\n' "$(basename "$file")" >&2
  fi
done

find "$EXPORT_DIR" -mindepth 1 -maxdepth 1 -type d -not -path "$ARCHIVE_DIR" -print0 |
while IFS= read -r -d '' package; do
  if open -a "$BOOKS_APP" "$package"; then
    mv -- "$package" "$ARCHIVE_DIR/"
    printf 'Handed off and archived package: %s\n' "$(basename "$package")"
  else
    printf 'Books handoff failed; leaving package in place: %s\n' "$(basename "$package")" >&2
  fi
done
