#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
cd "${ROOT}"

test -x skills/moox/scripts/cls_search.py
test -f skills/moox/references/cls-query.md
test -f skills/moox/references/cls-api.md
grep -Fq 'cls_search.py' skills/moox/SKILL.md
grep -Fq 'service_name' skills/moox/references/cls-query.md
grep -Fq -- '--topic' skills/moox/scripts/cls_search.py
grep -Fq -- '--since' skills/moox/scripts/cls_search.py
grep -Fq -- '--fields' skills/moox/scripts/cls_search.py

python3 -m py_compile skills/moox/scripts/cls_search.py
rm -rf skills/moox/scripts/__pycache__

printf 'PASS: MooX CLS query skill contract\n'
