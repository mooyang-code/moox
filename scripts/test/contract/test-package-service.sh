#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
PACKAGE_SCRIPT="${ROOT}/scripts/build/package-service.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/moox-package-service-test.XXXXXX")"
trap 'rm -rf -- "$TMP_ROOT"' EXIT

bash -n "$PACKAGE_SCRIPT"

service_dir="$TMP_ROOT/service"
output="$TMP_ROOT/release/service.zip"
mkdir -p "$service_dir/bin" "$service_dir/config"
printf '#!/bin/sh\n' >"$service_dir/start.sh"
printf '#!/bin/sh\n' >"$service_dir/stop.sh"
printf '#!/bin/sh\n' >"$service_dir/healthcheck.sh"
printf 'binary\n' >"$service_dir/bin/moox-admin"
printf 'port = 9528\n' >"$service_dir/config/app.toml"
printf 'notes\n' >"$service_dir/README.txt"

"$PACKAGE_SCRIPT" --service-dir "$service_dir" --output "$output"
unzip -tq "$output"
entries_file="$TMP_ROOT/entries"
unzip -Z1 "$output" >"$entries_file"
for entry in bin/moox-admin config/app.toml start.sh stop.sh healthcheck.sh README.txt; do
  grep -Fxq "$entry" "$entries_file" || {
    echo "missing ZIP entry: $entry" >&2
    exit 1
  }
done
if grep -Eq '(^|/)\.\.?(/|$)|^/|^\./' "$entries_file"; then
  echo 'ZIP contains an unsafe path' >&2
  exit 1
fi

mkdir -p "$TMP_ROOT/unsafe/bin" "$TMP_ROOT/unsafe/config" "$TMP_ROOT/unsafe/logs"
cp "$service_dir"/{start.sh,stop.sh,healthcheck.sh} "$TMP_ROOT/unsafe/"
cp "$service_dir/bin/moox-admin" "$TMP_ROOT/unsafe/bin/"
cp "$service_dir/config/app.toml" "$TMP_ROOT/unsafe/config/"
if "$PACKAGE_SCRIPT" --service-dir "$TMP_ROOT/unsafe" --output "$TMP_ROOT/unsafe.zip"; then
  echo 'runtime directories must be rejected' >&2
  exit 1
fi

mkdir -p "$TMP_ROOT/link/bin" "$TMP_ROOT/link/config"
cp "$service_dir"/{start.sh,stop.sh,healthcheck.sh} "$TMP_ROOT/link/"
cp "$service_dir/bin/moox-admin" "$TMP_ROOT/link/bin/"
cp "$service_dir/config/app.toml" "$TMP_ROOT/link/config/"
ln -s "$service_dir/bin/moox-admin" "$TMP_ROOT/link/bin/linked-binary"
if "$PACKAGE_SCRIPT" --service-dir "$TMP_ROOT/link" --output "$TMP_ROOT/link.zip"; then
  echo 'symbolic links must be rejected' >&2
  exit 1
fi

echo 'service package contract passed'
