#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

violations=()
scan_errors=()

# MooX standardizes every production entry context on tRPC, including shared
# packages and standalone binaries, so logging/metrics metadata is initialized.
while IFS= read -r module_root; do
	[[ -n "${module_root}" ]] || continue
	if [[ ! -d "${module_root}" ]]; then
		scan_errors+=("go.work module directory does not exist: ${module_root}")
		continue
	fi
	set +e
	matches=$(rg -n 'context\.Background\(\)' "${module_root}" \
		--glob '*.go' \
		--glob '!**/*_test.go' \
		--glob '!**/*.pb.go' \
		--glob '!**/*.trpc.go')
	status=$?
	set -e
	if (( status > 1 )); then
		scan_errors+=("failed to scan ${module_root} (rg exit ${status})")
		continue
	fi
	while IFS= read -r match; do
		[[ -n "${match}" ]] || continue
		violations+=("${match}: use trpc.BackgroundContext() in production code")
	done <<<"${matches}"
done < <(awk '
	/^use \(/ { in_use = 1; next }
	in_use && /^\)/ { exit }
	in_use { sub(/^[[:space:]]+/, ""); if ($0 != "") print $0 }
' go.work)

while IFS= read -r match; do
	case "${match}" in
		modules/factor/internal/rpc/recalc.go:* | \
		modules/collector/internal/reporter/task_status.go:* | \
		modules/storage/internal/infra/eventbus/producer_bus.go:*) ;;
		*) violations+=("${match}: CloneContext is only allowed at reviewed detached-work boundaries") ;;
	esac
done < <(rg -n --glob '*.go' --glob '!**/*_test.go' 'CloneContext\(' modules packages web-host)

if (( ${#scan_errors[@]} > 0 )); then
	printf 'tRPC context scan errors:\n  %s\n' "${scan_errors[@]}" >&2
	exit 1
fi

if (( ${#violations[@]} > 0 )); then
	{
		echo "tRPC context violations:"
		printf '  %s\n' "${violations[@]}"
		echo
		echo "Use trpc.CloneContext(ctx) before fire-and-forget work derived from a request."
	} >&2
	exit 1
fi

echo "==> tRPC context check passed"
