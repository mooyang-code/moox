#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

violations=()

# MooX standardizes every production entry context on tRPC, including shared
# packages and standalone binaries, so logging/metrics metadata is initialized.
while IFS= read -r module_root; do
	[[ -n "${module_root}" ]] || continue
	while IFS= read -r match; do
		violations+=("${match}: use trpc.BackgroundContext() in production code")
	done < <(rg -n 'context\.Background\(\)' "${module_root}" \
		--glob '*.go' \
		--glob '!**/*_test.go' \
		--glob '!**/*.pb.go' \
		--glob '!**/*.trpc.go' || true)
done < <(awk '
	/^use \(/ { in_use = 1; next }
	in_use && /^\)/ { exit }
	in_use { sub(/^[[:space:]]+/, ""); if ($0 != "") print $0 }
' go.work)

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
