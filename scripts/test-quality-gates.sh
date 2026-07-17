#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

grep -Eq '^check-boundaries:.*check-module-boundaries.*check-package-boundaries' Makefile
grep -Eq '^check-format:' Makefile
grep -Eq '^check-lint:' Makefile
grep -Eq '^check-context:' Makefile
grep -Eq '^verify:.*check-context.*check-format.*check-lint' Makefile

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

grep -Fq 'gofmt -l' scripts/check-gofmt.sh
grep -Fq 'git ls-files' scripts/check-gofmt.sh
grep -Fq ':!web-host/internal/statik/statik.go' scripts/check-gofmt.sh
grep -Fq "' go.work" scripts/check-trpc-context.sh
grep -Fq 'context\.Background\(\)' scripts/check-trpc-context.sh
grep -Fxq '/src/auto-import.d.ts' web/.prettierignore
grep -Fxq '/src/components.d.ts' web/.prettierignore

echo 'quality gate contract passed'
