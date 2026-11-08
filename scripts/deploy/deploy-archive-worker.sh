#!/usr/bin/env bash
# Installs moox-archive-worker as a systemd user unit on the Linux machine that
# runs it. The worker only dials out: to the fixed native Access for archive-mgr
# and Storage, and to a standard S3 object store for partition copies: either an
# external one (--object-store-endpoint) or the built-in SeaweedFS that ships in
# the same package (--builtin-object-store, managed as moox-seaweedfs.service).
# moox-cli unpacks the release package, uploads the credentials and runs this
# script (setup deploy-unit --profile archive-worker). The rendered config is
# checked with the new binary (check-config) before it replaces the old one and
# before the service restarts; a bad config leaves the old config and process alone.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
DEPLOY_DIR="${HOME}/moox/archive-worker"
SECRETS_DIR=""
ROOT_DIR=""
WORKER_ID=""
ACCESS_ADDRESS=""
ACCESS_ID=""
ACCESS_KEY_ID=""
BINARY=""
HEALTH_ENV=""
EXPORT_FORMAT="csv.gz"
PAGE_ROWS="500"
SKIP_BUILD=0
NO_START=0
OS_ENDPOINT=""
OS_REGION=""
OS_BUCKET=""
OS_PREFIX="moox-archive/"
OS_URL_STYLE=""
OS_USE_TLS="true"
OS_STORAGE_CLASS=""
BUILTIN_OS=0
SW_DIR=""
SYSTEMD_USER_DIR="${MOOX_SYSTEMD_USER_DIR:-${HOME}/.config/systemd/user}"

usage() {
  cat <<'EOF2'
Usage:
  scripts/deploy/deploy-archive-worker.sh --root-dir DIR --access-address HOST:PORT --access-id access@HOST \
    --access-key-id KEY_ID [options]

Options:
  --dir <path>              Install directory (binary, config, logs). Default: ~/moox/archive-worker.
  --root-dir <path>         Archive root: partition files and the .state staging database live here. Absolute path, required.
  --secrets-dir <path>      Directory holding the worker credentials. Default: <dir>/secrets.
  --worker-id <id>          Stable worker id. Default: archive-worker@<short hostname>. Keep it when moving the archive root to another machine.
  --access-address <addr>   Fixed native Access host:port for archive-mgr and Storage.
  --access-id <id>          Access identity, e.g. access@storage.
  --access-key-id <id>      KeyID assigned by Admin keys export --caller archive-worker.
  --binary <path>           Install this prebuilt moox-archive-worker (from the release package).
  --health-env <path>       Health credential file (MOOX_HEALTH_AUTH_* variables, mode 0600) rendered by moox-cli.
                            Required for the first deployment; later deployments keep the installed one.
  --export-format <fmt>     Partition file format: csv.gz (default) or csv.
  --page-rows <n>           Rows per Storage read page, 1..2000. Default: 500.
  --skip-build              Reuse <dir>/bin/moox-archive-worker.
  --no-start                Install files only; do not (re)start the service.

Built-in object store (mutually exclusive with --object-store-endpoint):
  --builtin-object-store                    Install and use the bundled SeaweedFS (weed next to --binary, or the installed one)
  --seaweedfs-dir <path>                    SeaweedFS home (bin, data, config). Default: <parent of --dir>/seaweedfs

External object store copies (all optional; with neither option object store copies are off):
  --object-store-endpoint <host[:port]>     S3 endpoint without scheme, e.g. cos.ap-nanjing.myqcloud.com
  --object-store-region <region>            Required when enabled (COS: ap-nanjing, R2: auto)
  --object-store-bucket <bucket>            Required when enabled (COS bucket names carry the APPID)
  --object-store-prefix <prefix>            Key prefix ending in /. Default: moox-archive/
  --object-store-url-style virtual|path     Required when enabled
  --object-store-use-tls true|false         Default: true
  --object-store-storage-class <class>      Sent as x-amz-storage-class, e.g. STANDARD_IA. Default: bucket default

Credentials expected in the secrets directory (regular files, mode 0600):
  access-archive-worker.key          External archive-worker signing key exported by Admin
  storage-primary-auth.secret        MOOX_STORAGE_PRIMARY_AUTH_SECRET of the Storage deployment
  archive-object-store.env           Only with --object-store-endpoint: ACCESS_KEY_ID= / SECRET_ACCESS_KEY= lines
                                     (projected by moox-cli from the Admin secret named in [archive_object_store])
EOF2
}

fail() {
  printf '[deploy-archive-worker] ERROR: %s\n' "$*" >&2
  exit 1
}

log() {
  printf '[deploy-archive-worker] %s\n' "$*"
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DEPLOY_DIR="$2"; shift 2 ;;
    --root-dir) ROOT_DIR="$2"; shift 2 ;;
    --secrets-dir) SECRETS_DIR="$2"; shift 2 ;;
    --worker-id) WORKER_ID="$2"; shift 2 ;;
    --access-address) ACCESS_ADDRESS="$2"; shift 2 ;;
    --access-id) ACCESS_ID="$2"; shift 2 ;;
    --access-key-id) ACCESS_KEY_ID="$2"; shift 2 ;;
    --binary) BINARY="$2"; shift 2 ;;
    --health-env) HEALTH_ENV="$2"; shift 2 ;;
    --export-format) EXPORT_FORMAT="$2"; shift 2 ;;
    --page-rows) PAGE_ROWS="$2"; shift 2 ;;
    --object-store-endpoint) OS_ENDPOINT="$2"; shift 2 ;;
    --object-store-region) OS_REGION="$2"; shift 2 ;;
    --object-store-bucket) OS_BUCKET="$2"; shift 2 ;;
    --object-store-prefix) OS_PREFIX="$2"; shift 2 ;;
    --object-store-url-style) OS_URL_STYLE="$2"; shift 2 ;;
    --object-store-use-tls) OS_USE_TLS="$2"; shift 2 ;;
    --object-store-storage-class) OS_STORAGE_CLASS="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    --no-start) NO_START=1; shift ;;
    --builtin-object-store) BUILTIN_OS=1; shift ;;
    --seaweedfs-dir) SW_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; fail "unknown option: $1" ;;
  esac
done

[[ "${ROOT_DIR}" = /* && "${ROOT_DIR}" != *$'\n'* ]] || fail "--root-dir must be an absolute path"
[[ "${ACCESS_ADDRESS}" =~ ^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9.-]+):[0-9]+$ ]] || fail "--access-address must be host:port"
[[ "${ACCESS_ID}" =~ ^access@[a-z][a-z0-9]*(-[a-z0-9]+)*$ ]] || fail "--access-id must be access@host"
[[ "${ACCESS_KEY_ID}" =~ ^[A-Za-z0-9._@:-]+$ && ${#ACCESS_KEY_ID} -le 128 ]] || fail "--access-key-id must be the assigned credential identifier"
[[ "${EXPORT_FORMAT}" == "csv" || "${EXPORT_FORMAT}" == "csv.gz" ]] || fail "--export-format must be csv or csv.gz"
[[ "${PAGE_ROWS}" =~ ^[0-9]+$ && "${PAGE_ROWS}" -ge 1 && "${PAGE_ROWS}" -le 2000 ]] || fail "--page-rows must be 1..2000"
[[ "${BUILTIN_OS}" -eq 0 || -z "${OS_ENDPOINT}" ]] || fail "--builtin-object-store and --object-store-endpoint are mutually exclusive"
if [[ -n "${OS_ENDPOINT}" ]]; then
  # 只做最基本的格式检查，严格校验由 worker 的 check-config 负责。
  [[ "${OS_ENDPOINT}" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || fail "--object-store-endpoint must be host[:port] without a scheme"
  [[ "${OS_REGION}" =~ ^[A-Za-z0-9_-]+$ ]] || fail "--object-store-region is required"
  [[ "${OS_BUCKET}" =~ ^[a-z0-9][a-z0-9.-]+$ ]] || fail "--object-store-bucket is required"
  [[ "${OS_URL_STYLE}" == "virtual" || "${OS_URL_STYLE}" == "path" ]] || fail "--object-store-url-style must be virtual or path"
  [[ "${OS_USE_TLS}" == "true" || "${OS_USE_TLS}" == "false" ]] || fail "--object-store-use-tls must be true or false"
  [[ "${OS_PREFIX}" =~ ^[A-Za-z0-9._/-]+/$ ]] || fail "--object-store-prefix must end with /"
  [[ -z "${OS_STORAGE_CLASS}" || "${OS_STORAGE_CLASS}" =~ ^[A-Z_]+$ ]] || fail "--object-store-storage-class must be like STANDARD_IA"
fi
mkdir -p "${DEPLOY_DIR}"
DEPLOY_DIR="$(cd "${DEPLOY_DIR}" && pwd -P)"
SECRETS_DIR="${SECRETS_DIR:-${DEPLOY_DIR}/secrets}"
if [[ -z "${WORKER_ID}" ]]; then
  WORKER_ID="archive-worker@$(hostname -s)"
fi
[[ "${WORKER_ID}" =~ ^[A-Za-z0-9@._-]+$ ]] || fail "worker id contains unsupported characters"

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi
}

require_secret() {
  local path="${SECRETS_DIR}/$1"
  [[ -f "${path}" && ! -L "${path}" ]] || fail "missing credential ${path}"
  [[ "$(file_mode "${path}")" == 600 ]] || fail "credential ${path} must have mode 0600"
  grep -q '[^[:space:]]' "${path}" || fail "credential ${path} is empty"
}

mkdir -p "${DEPLOY_DIR}/bin" "${DEPLOY_DIR}/config" "${DEPLOY_DIR}/logs" "${ROOT_DIR}"
mkdir -p "${SECRETS_DIR}"
chmod 0700 "${SECRETS_DIR}"
for secret in access-archive-worker.key storage-primary-auth.secret; do
  require_secret "${secret}"
done
if [[ -n "${OS_ENDPOINT}" ]]; then
  require_secret archive-object-store.env
fi

# ---------------------------------------------------------------- 内置对象存储（SeaweedFS）
# 端口固定、只听 127.0.0.1；gRPC 端口为 HTTP 端口 + 10000。
SW_MASTER=11460
SW_FILER=11462
SW_S3=11463
SW_PORTS=(11460 11461 11462 11463 21460 21461 21462 21463)
SW_BUCKET=moox-archive
SW_PREFIX=moox-archive/
SW_UNIT=moox-seaweedfs.service
SW_DIR="${SW_DIR:-$(dirname "${DEPLOY_DIR}")/seaweedfs}"

# weed shell 的退出码不可信、master 连不上会卡住：外层限时，按输出文本判断。
sw_shell() {
  printf '%s\n' "$1" | timeout 20 "${SW_DIR}/bin/weed" shell -master="127.0.0.1:${SW_MASTER}" -filer="127.0.0.1:${SW_FILER}" 2>&1 || true
}

sw_listening() {
  [[ -n "$(ss -ltnH "sport = :$1" 2>/dev/null)" ]]
}

if [[ "${BUILTIN_OS}" -eq 1 ]]; then
  sw_weed=""
  if [[ -n "${BINARY}" ]]; then
    sw_weed="$(dirname "${BINARY}")/weed"
    [[ -x "${sw_weed}" ]] || fail "missing ${sw_weed}: the archive-worker package must carry the built-in SeaweedFS"
  else
    [[ -x "${SW_DIR}/bin/weed" ]] || fail "missing ${SW_DIR}/bin/weed; deploy with --binary from the release package"
  fi
  mkdir -p "${SW_DIR}/bin" "${SW_DIR}/data" "${SW_DIR}/config"
  chmod 0700 "${SW_DIR}/config"
  # 访问密钥只在本机生成一次，之后一直沿用；s3.json 与 worker 的凭据都由它派生。
  sw_keys="${SW_DIR}/config/s3-credentials.env"
  if [[ ! -s "${sw_keys}" ]]; then
    (umask 077
      printf 'ACCESS_KEY_ID=MOOX%s\nSECRET_ACCESS_KEY=%s\n' \
        "$(od -An -tx1 -N8 /dev/urandom | tr -d ' \n' | tr 'a-f' 'A-F')" "$(od -An -tx1 -N20 /dev/urandom | tr -d ' \n')" \
        >"${sw_keys}.new" && mv -f "${sw_keys}.new" "${sw_keys}")
    log "generated the built-in object store access key"
  fi
  sw_access="$(sed -n 's/^ACCESS_KEY_ID=//p' "${sw_keys}")"
  sw_secret="$(sed -n 's/^SECRET_ACCESS_KEY=//p' "${sw_keys}")"
  [[ "${sw_access}" =~ ^[A-Za-z0-9]+$ && "${sw_secret}" =~ ^[A-Za-z0-9]+$ ]] || fail "invalid ${sw_keys}"
  # worker 凭据先写成 .new：check-config 读它，SeaweedFS 就绪后才和配置、二进制一起替换。
  (umask 077; cp "${sw_keys}" "${SECRETS_DIR}/archive-object-store.env.new")
  OS_ENDPOINT="127.0.0.1:${SW_S3}"
  OS_REGION=us-east-1
  OS_BUCKET="${SW_BUCKET}"
  OS_PREFIX="${SW_PREFIX}"
  OS_URL_STYLE=path
  OS_USE_TLS=false
  OS_STORAGE_CLASS=""
fi

# 安装并（重新）启动内置对象存储，等它就绪并建好桶（check-config 通过之后才调用）。
# 每次部署都重启：s3.json 不热加载，重启只要几秒，worker 的上传会自己重试。
install_seaweedfs() {
  if [[ -n "${sw_weed}" ]]; then
    install -m 0755 "${sw_weed}" "${SW_DIR}/bin/weed.new"
    mv -f "${SW_DIR}/bin/weed.new" "${SW_DIR}/bin/weed"
  fi
  install -m 0644 "${ROOT}/third_party/seaweedfs/LICENSE" "${SW_DIR}/LICENSE"
  # SeaweedFS 的简单动作 Write 含删除，所以身份不给动作、只挂策略：在前缀下读写、列举桶，不能删除。
  (umask 077; cat >"${SW_DIR}/config/s3.json.new" <<EOF2
{
  "identities": [
    {
      "name": "moox-archive-worker",
      "credentials": [{"accessKey": "${sw_access}", "secretKey": "${sw_secret}"}],
      "actions": [],
      "policy_names": ["moox-archive-writer"]
    }
  ],
  "policies": [
    {
      "name": "moox-archive-writer",
      "content": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":[\"s3:PutObject\",\"s3:GetObject\"],\"Resource\":[\"arn:aws:s3:::${SW_BUCKET}/${SW_PREFIX}*\"]},{\"Effect\":\"Allow\",\"Action\":[\"s3:ListBucket\"],\"Resource\":[\"arn:aws:s3:::${SW_BUCKET}\"]}]}"
    }
  ]
}
EOF2
  )
  mv -f "${SW_DIR}/config/s3.json.new" "${SW_DIR}/config/s3.json"
  mkdir -p "${SYSTEMD_USER_DIR}"
  sed -e "s#__SW__#${SW_DIR}#g" "${ROOT}/deploy/systemd/user/moox-seaweedfs.service.tmpl" >"${SYSTEMD_USER_DIR}/${SW_UNIT}"

  if ! systemctl --user is-active --quiet "${SW_UNIT}"; then
    for port in "${SW_PORTS[@]}"; do
      if sw_listening "${port}"; then
        fail "port ${port} needed by the built-in object store is in use: $(ss -ltnpH "sport = :${port}" 2>/dev/null)"
      fi
    done
  fi
  systemctl --user daemon-reload
  systemctl --user enable "${SW_UNIT}"
  systemctl --user restart "${SW_UNIT}"

  log "wait for the built-in object store"
  local ready=0
  for _ in $(seq 1 45); do
    if sw_listening "${SW_S3}" && sw_shell cluster.check | grep -q 'volume server .* \.\.\. ok'; then
      ready=1
      break
    fi
    sleep 2
  done
  if [[ "${ready}" -ne 1 ]]; then
    journalctl --user -u "${SW_UNIT}" -n 30 --no-pager >&2 || true
    fail "the built-in object store did not become ready"
  fi
  if ! sw_shell s3.bucket.list | grep -Eq "^[[:space:]]*${SW_BUCKET}[[:space:]]"; then
    out="$(sw_shell "s3.bucket.create -name ${SW_BUCKET}")"
    if ! grep -Eq 'already exists' <<<"${out}" && ! sw_shell s3.bucket.list | grep -Eq "^[[:space:]]*${SW_BUCKET}[[:space:]]"; then
      fail "could not create bucket ${SW_BUCKET}: ${out}"
    fi
  fi
  log "built-in object store ready at 127.0.0.1:${SW_S3} (bucket ${SW_BUCKET})"
}

# 健康检查凭据由 moox-cli 按集群运行身份渲染后传入（Monitor 经中转探测时用同一把）；重复部署不带 --health-env 时沿用已有的。
health_env="${SECRETS_DIR}/health-auth.env"
if [[ -n "${HEALTH_ENV}" ]]; then
  [[ -f "${HEALTH_ENV}" && ! -L "${HEALTH_ENV}" ]] || fail "missing --health-env file ${HEALTH_ENV}"
  [[ "$(file_mode "${HEALTH_ENV}")" == 600 ]] || fail "--health-env file ${HEALTH_ENV} must have mode 0600"
  for var in MOOX_HEALTH_AUTH_VERSION MOOX_HEALTH_AUTH_ACCESS_KEY MOOX_HEALTH_AUTH_SECRET_KEY; do
    grep -Eq "^${var}=[^[:space:]]+$" "${HEALTH_ENV}" || fail "--health-env file lacks ${var}"
  done
  (umask 077; cp "${HEALTH_ENV}" "${health_env}.new" && mv -f "${health_env}.new" "${health_env}")
fi
[[ -s "${health_env}" ]] || fail "--health-env is required for the first deployment"
health_access="$(sed -n 's/^MOOX_HEALTH_AUTH_ACCESS_KEY=//p' "${health_env}")"
health_secret="$(sed -n 's/^MOOX_HEALTH_AUTH_SECRET_KEY=//p' "${health_env}")"
[[ -n "${health_access}" && -n "${health_secret}" ]] || fail "invalid ${health_env}"

# 新二进制先放在 .new，和新配置一起经 check-config 校验通过后才替换。
candidate="${DEPLOY_DIR}/bin/moox-archive-worker"
if [[ -n "${BINARY}" ]]; then
  [[ -f "${BINARY}" ]] || fail "missing binary ${BINARY}"
  install -m 0755 "${BINARY}" "${DEPLOY_DIR}/bin/moox-archive-worker.new"
  candidate="${DEPLOY_DIR}/bin/moox-archive-worker.new"
fi
[[ -x "${candidate}" ]] || fail "missing ${candidate}"

config_new="${DEPLOY_DIR}/config/app.yaml.new"
umask 077
cat >"${config_new}" <<EOF2
# Rendered by scripts/deploy/deploy-archive-worker.sh; edit the flags, not this file.
gateway_client:
  access_address: "${ACCESS_ADDRESS}"
  access_id: "${ACCESS_ID}"
  caller: archive-worker
  key_id: "${ACCESS_KEY_ID}"
  key_file: ${SECRETS_DIR}/access-archive-worker.key

storage_auth_file: ${SECRETS_DIR}/storage-primary-auth.secret

worker:
  worker_id: ${WORKER_ID}
  root_dir: ${ROOT_DIR}
  export_format: ${EXPORT_FORMAT}
  min_free_bytes: 5GB
  heartbeat_interval: 10s
  pull_interval: 5s

staging:
  cache_size: 256MB

pacing:
  request_seconds: [25, 55]
  minute_window: true
  hour_quiet: ["58:00", "04:00"]
  request_interval: 200ms
  max_bytes_per_second: 512KB
  max_concurrent_runs: 4
  max_runs_per_data_node: 2
  page_rows: ${PAGE_ROWS}

health:
  addr: 127.0.0.1:11418

maintenance:
  at: "04:17:43"
EOF2
if [[ -n "${OS_ENDPOINT}" ]]; then
  cat >>"${config_new}" <<EOF2

object_store:
  endpoint: "${OS_ENDPOINT}"
  region: "${OS_REGION}"
  bucket: "${OS_BUCKET}"
  prefix: "${OS_PREFIX}"
  url_style: ${OS_URL_STYLE}
  use_tls: ${OS_USE_TLS}
  credentials_file: ${SECRETS_DIR}/archive-object-store.env
EOF2
  if [[ -n "${OS_STORAGE_CLASS}" ]]; then
    printf '  storage_class: %s\n' "${OS_STORAGE_CLASS}" >>"${config_new}"
  fi
fi
umask 022

# 内置模式下校验用的配置指向还没替换的 .new 凭据。
config_check="${config_new}"
if [[ "${BUILTIN_OS}" -eq 1 ]]; then
  config_check="${config_new}.check"
  (umask 077; sed "s#credentials_file: ${SECRETS_DIR}/archive-object-store.env\$#&.new#" "${config_new}" >"${config_check}")
fi
discard_candidates() {
  rm -f "${config_new}" "${config_new}.check" "${DEPLOY_DIR}/bin/moox-archive-worker.new" "${SECRETS_DIR}/archive-object-store.env.new"
}
if ! "${candidate}" check-config --config "${config_check}"; then
  discard_candidates
  fail "the rendered config did not pass check-config; the installed config, binary and service are unchanged"
fi
rm -f "${config_new}.check"
if [[ "${BUILTIN_OS}" -eq 1 ]]; then
  # worker 停用（--no-start）时也启动：它不持有任务状态，worker 恢复后直接可用。
  if ! (install_seaweedfs); then
    discard_candidates
    fail "the built-in object store is not ready; the installed worker config, credentials and binary are unchanged"
  fi
  mv -f "${SECRETS_DIR}/archive-object-store.env.new" "${SECRETS_DIR}/archive-object-store.env"
elif [[ -f "${SYSTEMD_USER_DIR}/${SW_UNIT}" ]]; then
  # 改用外部对象存储或不用对象存储：停掉内置的，数据留着。
  systemctl --user disable --now "${SW_UNIT}" || true
  log "stopped the built-in object store; its data stays in ${SW_DIR}"
fi
mv -f "${config_new}" "${DEPLOY_DIR}/config/app.yaml"
if [[ -n "${BINARY}" ]]; then
  mv -f "${DEPLOY_DIR}/bin/moox-archive-worker.new" "${DEPLOY_DIR}/bin/moox-archive-worker"
fi
if [[ -z "${OS_ENDPOINT}" ]]; then
  # 不启用对象存储：删掉残留的凭据，免得留下用不到的密钥。
  rm -f "${SECRETS_DIR}/archive-object-store.env"
fi

render_template() {
  sed -e "s#__ROOT__#${DEPLOY_DIR}#g" -e "s#__SECRETS__#${SECRETS_DIR}#g" -e "s#__HEALTH_ACCESS_KEY__#${health_access}#g" \
    -e "s#__HEALTH_SECRET_KEY__#${health_secret}#g" "$1"
}

mkdir -p "${SYSTEMD_USER_DIR}"
render_template "${ROOT}/deploy/systemd/user/moox-archive-worker.service.tmpl" >"${SYSTEMD_USER_DIR}/moox-archive-worker.service"
if command -v loginctl >/dev/null 2>&1 && [[ "$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null)" != yes ]]; then
  log "warning: user lingering is off; the worker stops at logout (enable with: loginctl enable-linger $(id -un))"
fi
if [[ "${NO_START}" -eq 0 ]]; then
  systemctl --user daemon-reload
  systemctl --user enable moox-archive-worker.service
  systemctl --user restart moox-archive-worker.service
fi

if [[ "${NO_START}" -eq 1 ]]; then
  log "installed to ${DEPLOY_DIR} (not started)"
  exit 0
fi

log "wait for moox-archive-worker readiness"
for _ in $(seq 1 90); do
  if env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
    "${DEPLOY_DIR}/bin/moox-archive-worker" health >/dev/null 2>&1; then
    log "moox-archive-worker is ready (${WORKER_ID})"
    exit 0
  fi
  sleep 2
done
env -i PATH=/usr/bin:/bin MOOX_HEALTH_AUTH_ACCESS_KEY="${health_access}" MOOX_HEALTH_AUTH_SECRET_KEY="${health_secret}" \
  "${DEPLOY_DIR}/bin/moox-archive-worker" health || true
tail -n 30 "${DEPLOY_DIR}/logs/archive-worker.log" >&2 || true
fail "moox-archive-worker did not become ready"
