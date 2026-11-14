#!/bin/sh
# Public-repo hygiene: no token prefixes and no e-mail addresses in the tree.
# The legacy test.sh greps for the token prefix itself and is allowed to name it.
set -u
cd "$(dirname "$0")/.."
fail=0
prefix="sk""-ant"
if git ls-files -z | xargs -0 grep -nI "$prefix" -- 2>/dev/null | grep -v '^test.sh:'; then
  echo "hygiene: a token prefix is in the tree" >&2
  fail=1
fi
# E-mail addresses. Allowed: the GitHub Actions bot address used by the release workflow, and
# documentation addresses at example.com (RFC 2606) in the legacy script.
if git ls-files -z | xargs -0 grep -nIE '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}' -- 2>/dev/null \
    | grep -vE 'github-actions\[bot\]@users\.noreply\.github\.com|@([a-z]+\.)?example\.com'; then
  echo "hygiene: an e-mail address is in the tree" >&2
  fail=1
fi
[ "$fail" = 0 ] && echo "hygiene: ok"
exit "$fail"
