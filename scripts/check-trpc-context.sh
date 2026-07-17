#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

violations=()

# Only tRPC-enabled modules are subject to this rule. Transport-neutral modules
# such as packages/jetstream and modules/gateway intentionally keep the standard
# library context and must not gain a framework dependency just for this check.
for module_file in modules/*/go.mod packages/*/go.mod; do
	[[ -f "${module_file}" ]] || continue
	rg -q 'trpc\.group/trpc-go/trpc-go' "${module_file}" || continue
	module_root="${module_file%/go.mod}"
	while IFS= read -r match; do
		violations+=("${match}: use trpc.BackgroundContext() in tRPC-enabled production code")
	done < <(rg -n 'context\.Background\(\)' "${module_root}" \
		--glob '*.go' \
		--glob '!**/*_test.go' \
		--glob '!**/*.pb.go' \
		--glob '!**/*.trpc.go' || true)
done

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
