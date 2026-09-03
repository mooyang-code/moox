#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"

(cd "${ROOT}/packages/doctor" && go test -count=1 ./...)
grep -q 'moox_gateway' "${ROOT}/config/setup/service-deployments.yaml"
for contract in \
  'packages/doctor/components.yaml' \
  'packages/doctor/report.schema.json' \
  'modules/cli/config/cli.yaml'; do
  grep -q "${contract}" "${ROOT}/scripts/release/release.sh" || {
    echo "missing Doctor release contract: ${contract}" >&2
    exit 1
  }
  grep -q "${contract}" "${ROOT}/scripts/deploy/deploy-moox.sh" || {
    echo "missing Doctor deploy contract: ${contract}" >&2
    exit 1
  }
done
grep -q 'config/setup/dataset-health-policy.yaml' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'config/setup/collector-rules.yaml' "${ROOT}/scripts/deploy/deploy-moox.sh"

unfrozen="--no-""frozen-lockfile"
floating_statik="statik@""latest"
if rg -n "pnpm install .*${unfrozen}|${floating_statik}" "${ROOT}/scripts" "${ROOT}/web-host/Makefile"; then
  echo "release tooling contains an unpinned dependency" >&2
  exit 1
fi

grep -q 'set -euo pipefail' "${ROOT}/scripts/release/release.sh"
grep -q 'SKIP_WEB_ASSETS' "${ROOT}/scripts/release/release.sh"
grep -q 'binary_name' "${ROOT}/scripts/build/build.sh"
grep -q 'collector-scf)' "${ROOT}/scripts/build/build.sh"
! grep -q 'collector-market-data-scf' "${ROOT}/scripts/build/build.sh"
grep -q 'moox-collector-scf' "${ROOT}/scripts/build/build-release-binaries.sh"
grep -q 'release/moox-binaries-' "${ROOT}/scripts/build/build-release-binaries.sh"
grep -q 'publish-release-binaries.sh' "${ROOT}/scripts/build/build-release-binaries.sh"
grep -q -- '--artifact' "${ROOT}/scripts/release/publish-release-binaries.sh"
grep -q -- '--restart' "${ROOT}/scripts/release/publish-release-binaries.sh"
grep -q 'checksum mismatch' "${ROOT}/scripts/release/publish-release-binaries.sh"
grep -q 'manifest.txt' "${ROOT}/scripts/release/publish-release-binaries.sh"
grep -q 'release-binaries' "${ROOT}/Makefile"
grep -q 'RELEASE_PLATFORMS' "${ROOT}/scripts/release/release-matrix.sh"
grep -q 'github.com/rakyll/statik@v0.1.7' "${ROOT}/scripts/release/release.sh"
grep -q 'go run github.com/rakyll/statik@v0.1.7' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'deploy/caddy/Caddyfile.public' "${ROOT}/scripts/release/release.sh"
grep -q 'deploy/caddy/Caddyfile.public.no-admin' "${ROOT}/scripts/release/release.sh"
grep -q 'caddy-managed.sh" start --deploy-dir "${ROOT}" 9>&-' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'Caddyfile.next" 8>&-' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q '"${DEPLOY_DIR}/start.sh" 8>&-' "${ROOT}/scripts/deploy/deploy-moox.sh"
grep -q 'modules/trade/config/.' "${ROOT}/scripts/release/release.sh"
grep -q 'RELEASE_ROOT}/modules/trade/config' "${ROOT}/scripts/release/release.sh"
grep -q 'RELEASE_ROOT}/modules/cli/bin' "${ROOT}/scripts/release/release.sh"
grep -q 'RELEASE_ROOT}/modules/storage-primary/bin' "${ROOT}/scripts/release/release.sh"
grep -q 'config/setup/.' "${ROOT}/scripts/release/release.sh"
grep -q 'moox.toml.example' "${ROOT}/scripts/release/release.sh"
if rg -n 'RELEASE_ROOT}/(cli|admin|gateway|eventbus|web-host|cloudnode|collector|factor|strategy|trade|monitor|storage-primary|storage-view|archive|hostagent)(/|"|$)' "${ROOT}/scripts/release/release.sh"; then
  echo "release archive must place service artifacts under modules/" >&2
  exit 1
fi
grep -q 'build_go modules/strategy ./cmd/server moox-strategy' "${ROOT}/scripts/build/build.sh"
grep -q 'build_go modules/strategy ./cmd/cli moox-strategy-cli' "${ROOT}/scripts/build/build.sh"
for contract in \
  'RELEASE_ROOT}/modules/strategy/bin' \
  'copy_binary moox-strategy ' \
  'copy_binary moox-strategy-cli ' \
  'modules/strategy/config/.' \
  'RELEASE_ROOT}/modules/factor/python-runtime/'; do
  grep -q "${contract}" "${ROOT}/scripts/release/release.sh" || {
    echo "missing Strategy release contract: ${contract}" >&2
    exit 1
  }
done
tmp_strategy="$(mktemp -d "${TMPDIR:-/tmp}/moox-strategy-release.XXXXXX")"
trap 'rm -rf "${tmp_strategy}"' EXIT
mkdir -p \
  "${tmp_strategy}/factor/python-runtime" \
  "${tmp_strategy}/strategy"
cp -R "${ROOT}/packages/pyruntime/python/." "${tmp_strategy}/factor/python-runtime/"
PYTHONPATH="${tmp_strategy}/factor/python-runtime" python3 -c 'from moox_pyruntime import protocol; assert protocol.TYPE_RUN'
grep -q "name __pycache__ -o -name .pytest_cache" "${ROOT}/scripts/release/release.sh"
grep -q "name '\*.pyc' -o -name '\*.sqlite' -o -name '\*.db'" "${ROOT}/scripts/release/release.sh"

release="${tmp_strategy}/release"
mkdir -p "${release}/config/setup" "${release}/examples"
cp -R "${ROOT}/examples/." "${release}/examples/"
cp -R "${ROOT}/config/setup/." "${release}/config/setup/"
for path in \
  'config/setup/metadata.yaml' \
  'config/setup/dataset-health-policy.yaml' \
  'config/setup/service-deployments.yaml' \
  'config/setup/collector-rules.yaml'; do
  test -f "${release}/${path}" || {
    echo "release is missing default setup file: ${path}" >&2
    exit 1
  }
done
test ! -e "${release}/examples/monitor-pipelines.yaml"
test ! -e "${release}/examples/metadata-quant-initial.seed.yaml"

bash -n \
  "${ROOT}/scripts/build/build.sh" \
  "${ROOT}/scripts/build/build-release-binaries.sh" \
  "${ROOT}/scripts/release/publish-release-binaries.sh" \
  "${ROOT}/scripts/test/contract/test-deploy-moox-factor.sh" \
  "${ROOT}/scripts/runtime/moox-factor-run-once.sh" \
  "${ROOT}/scripts/build/package-service.sh" \
  "${ROOT}/scripts/release/release.sh" \
  "${ROOT}/scripts/release/release-matrix.sh"

tmp_binary_release="$(mktemp -d "${TMPDIR:-/tmp}/moox-binary-release.XXXXXX")"
trap 'rm -rf "${tmp_strategy}" "${tmp_binary_release}"' EXIT
mkdir -p "${tmp_binary_release}/bin"
printf 'fixture\n' >"${tmp_binary_release}/bin/moox-fixture"
publish_output="$("${ROOT}/scripts/release/publish-release-binaries.sh" \
  --artifact "${tmp_binary_release}" \
  --target user@example.invalid \
  --dir /srv/moox \
  --binary moox-fixture \
  --dry-run)"
grep -q 'publish 1 binaries' <<<"${publish_output}"
grep -q 'moox-fixture' <<<"${publish_output}"
"${ROOT}/scripts/test/contract/test-package-service.sh"
"${ROOT}/scripts/test/contract/test-deploy-moox-factor.sh"
matrix_output="$(VERSION=test RELEASE_PLATFORMS=linux/amd64,darwin/arm64,windows/amd64 "${ROOT}/scripts/release/release-matrix.sh" --dry-run)"
grep -q 'release test (linux/amd64' <<<"${matrix_output}"
grep -q 'release test (darwin/arm64' <<<"${matrix_output}"
grep -q 'release test (windows/amd64' <<<"${matrix_output}"
