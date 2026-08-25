#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

run_go_tests() {
  local module="$1"
  printf '\n==> go test -race %s\n' "$module"
  (cd "$module" && go test -race ./...)
}

for module in \
  packages/report \
  packages/notification \
  packages/events \
  modules/hostagent \
  modules/monitor \
  modules/collector \
  modules/cloudnode \
  modules/storage \
  modules/factor \
  modules/trade
do
  run_go_tests "$module"
done

printf '\n==> HostAgent release contract\n'
bash skills/moox/scripts/test-hostagent-release.sh

printf '\n==> HostAgent deploy contract\n'
bash skills/moox/scripts/test-hostagent-deploy.sh

printf '\n==> Monitor coverage contract\n'
bash scripts/test-monitor-coverage-contract.sh

printf '\nObservability local verification passed.\n'
printf 'Remote readiness, real BTC-USDT canary, and external notification delivery require deployment evidence.\n'
