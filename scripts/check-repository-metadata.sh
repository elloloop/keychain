#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

fail=0

check_codeowners() {
  local file="$ROOT/.github/CODEOWNERS"
  local line_no=0
  local line pattern owners target

  if [[ ! -f "$file" ]]; then
    echo "CODEOWNERS not found: $file" >&2
    fail=1
    return
  fi

  while IFS= read -r line || [[ -n "$line" ]]; do
    line_no=$((line_no + 1))
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" ]] && continue
    [[ "$line" == \#* ]] && continue

    pattern="${line%%[[:space:]]*}"
    owners="${line#"$pattern"}"
    owners="${owners#"${owners%%[![:space:]]*}"}"
    if [[ -z "$owners" ]]; then
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
    if [[ -n "$target" && ! -e "$ROOT/$target" ]]; then
      echo "CODEOWNERS:$line_no references missing path: $pattern" >&2
      fail=1
    fi
  done < "$file"
}

check_no_floating_container_tags() {
  local file rel matches
  while IFS= read -r file; do
    [[ -f "$file" ]] || continue
    rel="${file#"$ROOT/"}"
    matches="$(grep -nE 'type=raw,value=latest|pattern=\{\{major\}\}(\.\{\{minor\}\})?|:latest([^[:alnum:]_.-]|$)' "$file" || true)"
    if [[ -n "$matches" ]]; then
      while IFS= read -r match; do
        echo "$rel:$match uses a floating container image tag" >&2
      done <<< "$matches"
      fail=1
    fi
  done < <(
    find "$ROOT/.github/workflows" -type f \( -name '*.yml' -o -name '*.yaml' \)
    printf '%s\n' "$ROOT/README.md" "$ROOT/TESTING.md" "$ROOT/Dockerfile" "$ROOT/docker-compose.yml" "$ROOT/Makefile"
  )
}

check_codeowners
check_no_floating_container_tags

exit "$fail"
