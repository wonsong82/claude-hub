#!/bin/sh
# Render the Homebrew formula from the template.
#   render-formula.sh <version without v> <checksums.txt>  > claude-hub.rb
set -eu
version="$1"
sums="$2"
dir=$(cd "$(dirname "$0")" && pwd)
sha() {
  s=$(awk -v f="claude-hub_${version}_$1.tar.gz" '$2 == f { print $1 }' "$sums")
  [ -n "$s" ] || { echo "no checksum for $1 in $sums" >&2; exit 1; }
  printf '%s' "$s"
}
sed -e "s/@VERSION@/${version}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/$(sha darwin_arm64)/" \
    -e "s/@SHA256_DARWIN_AMD64@/$(sha darwin_amd64)/" \
    -e "s/@SHA256_LINUX_ARM64@/$(sha linux_arm64)/" \
    -e "s/@SHA256_LINUX_AMD64@/$(sha linux_amd64)/" \
    "$dir/claude-hub.rb.tmpl"
