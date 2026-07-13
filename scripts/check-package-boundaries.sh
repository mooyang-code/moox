#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

violations=()

# Domain and core packages contain business rules and must not depend on delivery
# or persistence details. Infrastructure is the opposite direction of the edge.
for root in modules/*/internal/domain modules/*/internal/core; do
	[[ -d "${root}" ]] || continue
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: domain/core packages must not import internal application, service, infra, or rpc packages")
	done < <(rg -n '"github.com/mooyang-code/moox/modules/[^/]+/internal/(infra|service|services|application|rpc)/' "${root}" --glob '*.go' || true)
done

for root in modules/*/internal/infra; do
	[[ -d "${root}" ]] || continue
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: infrastructure packages must not import internal application, service, or rpc packages")
	done < <(rg -n '"github.com/mooyang-code/moox/modules/[^/]+/internal/(service|services|application|rpc)/' "${root}" --glob '*.go' || true)
done

# Keep the naming decisions executable so a new module cannot silently restore
# the layouts that this refactor removes.
legacy_paths=(
	'internal/services/'
	'internal/gateway/spacecontext/'
	'internal/sources/exchangetypes/'
	'internal/providers/tencent-scf/'
	'proto/gen'
)
for path in "${legacy_paths[@]}"; do
	while IFS= read -r match; do
		file="${match%%:*}"
		rest="${match#*:}"
		line="${rest%%:*}"
		violations+=("${file}:${line}: legacy layout path '${path}' is not allowed")
	done < <(rg -n --fixed-strings "${path}" modules --glob '*.go' --glob '*.sh' --glob 'go.mod' --glob '*.proto' --glob 'Makefile' || true)
done

while IFS= read -r directory; do
	violations+=("${directory}: module-level tests must live under test/, not tests/")
done < <(find modules -mindepth 2 -maxdepth 2 -type d -name tests -print 2>/dev/null || true)

if (( ${#violations[@]} > 0 )); then
	{
		echo "package boundary violations:"
		printf '  %s\n' "${violations[@]}"
	} >&2
	exit 1
fi

echo "==> package boundaries passed"
