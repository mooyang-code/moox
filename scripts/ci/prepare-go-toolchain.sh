#!/usr/bin/env bash
set -euo pipefail

# Explicit provisioning, separate from build.sh. Go verifies toolchain modules
# through the checksum database; actual builds always set GOTOOLCHAIN=local.
required="${1:?pinned Go version is required}"
[[ "${required}" =~ ^1\.[0-9]+\.[0-9]+$ ]] || exit 2
[[ "$(uname -s)" == Linux ]] || { echo 'compile host must run Linux' >&2; exit 2; }
command -v gcc >/dev/null
command -v g++ >/dev/null
destination="${HOME}/.local/go${required}"
if [[ -x "${destination}/bin/go" ]]; then
  [[ "$(GOTOOLCHAIN=local "${destination}/bin/go" env GOVERSION)" == "go${required}" ]]
  exit 0
fi
[[ ! -e "${destination}" ]] || { echo 'toolchain destination is incomplete' >&2; exit 1; }
bootstrap_go="$(command -v go || true)"
if [[ -z "${bootstrap_go}" ]]; then
  for candidate in "${HOME}"/.local/go*/bin/go /usr/local/go/bin/go "${HOME}/go-sdk/bin/go"; do
    if [[ -x "${candidate}" ]]; then bootstrap_go="${candidate}"; break; fi
  done
fi
[[ -n "${bootstrap_go}" ]] || { echo 'install a bootstrap Go toolchain first' >&2; exit 1; }
cd /tmp
downloaded="$(GOWORK=off GOTOOLCHAIN="go${required}" GOSUMDB=sum.golang.org "${bootstrap_go}" env GOROOT)"
[[ -x "${downloaded}/bin/go" ]]
[[ "$(GOTOOLCHAIN=local "${downloaded}/bin/go" env GOVERSION)" == "go${required}" ]]
mkdir -p "${HOME}/.local"
staging="$(mktemp -d "${HOME}/.local/.go${required}.XXXXXX")"
trap 'rm -rf -- "${staging}"' EXIT
cp -a -- "${downloaded}/." "${staging}/"
[[ "$(GOTOOLCHAIN=local "${staging}/bin/go" env GOVERSION)" == "go${required}" ]]
mv -T -- "${staging}" "${destination}"
