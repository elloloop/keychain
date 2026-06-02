#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FILE="$ROOT/.github/CODEOWNERS"

if [ ! -f "$FILE" ]; then
  echo "CODEOWNERS not found: $FILE" >&2
  exit 1
fi

fail=0
line_no=0
while IFS= read -r line || [ -n "$line" ]; do
  line_no=$((line_no + 1))
  line="${line#"${line%%[![:space:]]*}"}"
  [ -z "$line" ] && continue
  [[ "$line" == \#* ]] && continue

  pattern="${line%%[[:space:]]*}"
  owners="${line#"$pattern"}"
  owners="${owners#"${owners%%[![:space:]]*}"}"
  if [ -z "$owners" ]; then
    echo "CODEOWNERS:$line_no missing owner for $pattern" >&2
    fail=1
  fi

  case "$pattern" in
    *"*"*|*"?"*|*"["*|*"]"*|*"!"*|"")
      continue
      ;;
  esac

  target="${pattern#/}"
  target="${target%/}"
  if [ -n "$target" ] && [ ! -e "$ROOT/$target" ]; then
    echo "CODEOWNERS:$line_no references missing path: $pattern" >&2
    fail=1
  fi
done < "$FILE"

exit "$fail"
