#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT_DIR"

go test ./modules/collector/test -run '^TestKlineResamplePipelineE2E$' -count=1 -v
