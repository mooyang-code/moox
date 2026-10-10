#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/build/package-service.sh --service-dir DIR --output FILE

Package a service directory for `moox-cli setup deploy-service`.

Required service files:
  bin/<binary>
  config/<config>
  start.sh
  stop.sh
  healthcheck.sh
EOF
}

die() {
  printf 'package-service: %s\n' "$1" >&2
  exit 1
}

service_dir=''
output=''
while (($# > 0)); do
  case "$1" in
    --service-dir)
      (($# >= 2)) || die 'missing value for --service-dir'
      service_dir=$2
      shift 2
      ;;
    --output)
      (($# >= 2)) || die 'missing value for --output'
      output=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "$service_dir" ]] || die '--service-dir is required'
[[ -n "$output" ]] || die '--output is required'
command -v zip >/dev/null 2>&1 || die 'zip is required'
command -v unzip >/dev/null 2>&1 || die 'unzip is required'

[[ -d "$service_dir" ]] || die "service directory does not exist: $service_dir"
service_dir=$(cd "$service_dir" && pwd -P)

output_dir=$(dirname "$output")
mkdir -p -- "$output_dir"
output_dir=$(cd "$output_dir" && pwd -P)
output="$output_dir/$(basename "$output")"
case "$output" in
  "$service_dir"/*)
    die 'output ZIP must not be inside the service directory'
    ;;
esac

for required in start.sh stop.sh healthcheck.sh; do
  [[ -f "$service_dir/$required" && ! -L "$service_dir/$required" ]] ||
    die "missing required file: $required"
done
[[ -d "$service_dir/bin" ]] || die 'missing required directory: bin'
[[ -d "$service_dir/config" ]] || die 'missing required directory: config'

binary=$(find "$service_dir/bin" -type f -print -quit)
[[ -n "$binary" ]] || die 'bin must contain at least one regular file'
config=$(find "$service_dir/config" -type f -print -quit)
[[ -n "$config" ]] || die 'config must contain at least one regular file'

symlink=$(find "$service_dir" -type l -print -quit)
[[ -z "$symlink" ]] || die "symbolic links are not allowed: ${symlink#"$service_dir"/}"

for blocked in data logs run secrets certs; do
  [[ ! -e "$service_dir/$blocked" && ! -L "$service_dir/$blocked" ]] ||
    die "runtime directory is not allowed in a service package: $blocked"
done

shopt -s dotglob nullglob
entries=("$service_dir"/*)
service_entries=()
for entry in "${entries[@]}"; do
  service_entries+=("${entry#"$service_dir"/}")
done
(( ${#service_entries[@]} > 0 )) || die 'service directory is empty'

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/moox-service-package.XXXXXX")
trap 'rm -rf -- "$tmp_dir"' EXIT
tmp_zip="$tmp_dir/service.zip"
(
  cd "$service_dir"
  zip -rq "$tmp_zip" -- "${service_entries[@]}"
)
unzip -tq "$tmp_zip"
mv -f -- "$tmp_zip" "$output"
chmod 0600 "$output"
printf 'service package created: %s\n' "$output"
