#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "${ROOT}"

node - <<'NODE'
const pkg = require('./web/package.json')
const expected = ['lint:eslint:check', 'lint:prettier:check']
for (const name of expected) {
  const command = pkg.scripts?.[name]
  if (!command) throw new Error(`missing web script: ${name}`)
  if (/--fix|--write/.test(command)) {
    throw new Error(`${name} must be read-only: ${command}`)
  }
}
if (!pkg.scripts['lint:eslint:check'].includes('--max-warnings 0')) {
  throw new Error('ESLint check must reject warnings')
}
NODE

check_format_script='scripts/check/check-gofmt.sh'
grep -Fq 'gofmt -l' "${check_format_script}"
grep -Fq 'git ls-files' "${check_format_script}"
grep -Fxq '/src/auto-import.d.ts' web/.prettierignore
grep -Fxq '/src/components.d.ts' web/.prettierignore
bash scripts/test/contract/test-secret-scan-contract.sh
bash scripts/check/check-report-logcls.sh

echo 'quality gate contract passed'
