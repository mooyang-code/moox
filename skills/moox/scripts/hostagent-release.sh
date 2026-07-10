#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VERSION="${VERSION:-dev}"
ARCH="${TARGET_GOARCH:-${GOARCH:-$(go env GOARCH)}}"
case "${ARCH}" in amd64|arm64) ;; *) echo "host agent supports amd64/arm64 only" >&2; exit 1 ;; esac
OUT="${ROOT}/release/moox-host-agent-${VERSION}-linux-${ARCH}"
rm -rf "${OUT}" "${OUT}.tar.gz"
mkdir -p "${OUT}/bin" "${OUT}/config" "${OUT}/systemd/user"
TARGET_GOOS=linux TARGET_GOARCH="${ARCH}" "${ROOT}/scripts/build.sh" hostagent
cp "${ROOT}/bin/moox-host-agent" "${OUT}/bin/"
cp "${ROOT}/bin/moox-host-agent-cli" "${OUT}/bin/"
cp "${ROOT}/modules/hostagent/config/app.yaml" "${OUT}/config/app.example.yaml"
cp "${ROOT}/modules/hostagent/config/eventbus.example.yaml" "${OUT}/config/eventbus.example.yaml"
cp "${ROOT}/modules/hostagent/config/trpc_go.yaml" "${OUT}/config/trpc_go.yaml"
cat > "${OUT}/systemd/user/moox-host-agent.service" <<'EOF'
[Unit]
Description=MooX Host Agent
After=network-online.target

[Service]
WorkingDirectory=%h/.local/lib/moox/hostagent/current
ExecStart=%h/.local/lib/moox/hostagent/current/bin/moox-host-agent -conf=%h/.local/lib/moox/hostagent/current/config/trpc_go.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
cp "${ROOT}/modules/hostagent/README.md" "${OUT}/README.md"
cp "${ROOT}/modules/hostagent/THIRD_PARTY_NOTICES.md" "${OUT}/THIRD_PARTY_NOTICES.md"
(cd "${OUT}" && sha256sum bin/* config/* > SHA256SUMS)
tar -C "${ROOT}/release" -czf "${OUT}.tar.gz" "$(basename "${OUT}")"
echo "${OUT}.tar.gz"
