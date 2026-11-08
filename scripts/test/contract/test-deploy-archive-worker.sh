#!/usr/bin/env bash
# Contract: deploy-archive-worker.sh validates its inputs, installs a prebuilt binary,
# renders the worker configuration (with an optional object_store section) and the service definition,
# checks the rendered config with check-config before replacing anything, and never starts the service with --no-start.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/scripts/deploy/deploy-archive-worker.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

fail() { echo "test-deploy-archive-worker: $*" >&2; exit 1; }

mkdir -p "${tmp}/deploy"
tmp="$(cd "${tmp}" && pwd -P)" # 脚本用 pwd -P 解析安装目录
deploy="${tmp}/deploy"
secrets="${deploy}/secrets"
archive_root="${tmp}/archive-root"
mkdir -p "${secrets}"
chmod 0700 "${secrets}"
printf 'k%.0s' {1..40} >"${secrets}/access-archive-worker.key"
printf 's%.0s' {1..40} >"${secrets}/storage-primary-auth.secret"
chmod 0600 "${secrets}"/*
# 假二进制：记下 check-config 校验的是哪份配置。
printf '#!/bin/sh\nif [ "$1" = check-config ]; then echo "$3" >>"%s/check-config.log"; fi\nexit 0\n' "${tmp}" >"${tmp}/moox-archive-worker"
chmod 0755 "${tmp}/moox-archive-worker"
# 只在 check-config 时失败的假二进制。
printf '#!/bin/sh\n[ "$1" = check-config ] && exit 1\nexit 0\n' >"${tmp}/bad-worker"
chmod 0755 "${tmp}/bad-worker"

health_env="${tmp}/health-auth.env"
printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=health-access-9\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$(printf 'h%.0s' {1..64})" >"${health_env}"
chmod 0600 "${health_env}"

export MOOX_SYSTEMD_USER_DIR="${tmp}/systemd"
args=(--dir "${deploy}" --root-dir "${archive_root}" --worker-id archive-worker@testhost --access-address access.example.test:11004
  --access-id access@storage --access-key-id key-123 --binary "${tmp}/moox-archive-worker" --health-env "${health_env}" --no-start)

# 输入校验。
if bash "${SCRIPT}" --dir "${deploy}" --root-dir relative/path --access-address a:1 --access-id access@x --access-key-id k --no-start >"${tmp}/bad1.log" 2>&1; then
  fail "a relative --root-dir must be rejected"
fi
if bash "${SCRIPT}" --dir "${deploy}" --root-dir "${archive_root}" --access-address nope --access-id access@x --access-key-id k --no-start >"${tmp}/bad2.log" 2>&1; then
  fail "a bad --access-address must be rejected"
fi
chmod 0644 "${secrets}/access-archive-worker.key"
if bash "${SCRIPT}" "${args[@]}" >"${tmp}/bad3.log" 2>&1; then
  fail "a credential with a loose mode must be rejected"
fi
chmod 0600 "${secrets}/access-archive-worker.key"

bash "${SCRIPT}" "${args[@]}" >"${tmp}/ok.log" 2>&1 || { cat "${tmp}/ok.log" >&2; fail "deploy with --no-start failed"; }

[[ -x "${deploy}/bin/moox-archive-worker" ]] || fail "binary was not installed"
[[ -d "${archive_root}" ]] || fail "archive root was not created"
config="${deploy}/config/app.yaml"
for want in 'access_address: "access.example.test:11004"' 'access_id: "access@storage"' 'caller: archive-worker' 'key_id: "key-123"' \
  "key_file: ${secrets}/access-archive-worker.key" "storage_auth_file: ${secrets}/storage-primary-auth.secret" 'worker_id: archive-worker@testhost' \
  "root_dir: ${archive_root}" 'addr: 127.0.0.1:11418' 'export_format: csv.gz' 'page_rows: 500'; do
  grep -qF -- "${want}" "${config}" || fail "rendered config misses: ${want}"
done
# worker 严格解析配置：已删除的配置项一旦渲染出来，worker 会启动失败。
for gone in 'export_formats' 'rebuild_ratio'; do
  grep -qF -- "${gone}" "${config}" && fail "rendered config still has removed key: ${gone}"
done
unit="${MOOX_SYSTEMD_USER_DIR}/moox-archive-worker.service"
[[ -s "${unit}" ]] || fail "service definition was not rendered"
grep -qF "${deploy}/bin/moox-archive-worker" "${unit}" || fail "service definition does not run the installed binary"
grep -q '__[A-Z_]*__' "${unit}" && fail "unrendered placeholder in the service definition"
[[ -s "${secrets}/health-auth.env" ]] || fail "health auth env was not installed"
grep -qF 'MOOX_HEALTH_AUTH_ACCESS_KEY=health-access-9' "${secrets}/health-auth.env" || fail "health auth env must come from --health-env"
[[ "$(stat -f '%Lp' "${secrets}/health-auth.env" 2>/dev/null || stat -c '%a' "${secrets}/health-auth.env")" == 600 ]] || fail "health auth env must be mode 0600"
# 再次部署不带 --health-env 时保留已安装的健康检查凭据。
before="$(cat "${secrets}/health-auth.env")"
bash "${SCRIPT}" --dir "${deploy}" --root-dir "${archive_root}" --worker-id archive-worker@testhost --access-address access.example.test:11004 \
  --access-id access@storage --access-key-id key-123 --skip-build --no-start >/dev/null 2>&1 || fail "second deploy failed"
[[ "$(cat "${secrets}/health-auth.env")" == "${before}" ]] || fail "health credentials must be kept across deployments"

grep -qF "${deploy}/config/app.yaml.new" "${tmp}/check-config.log" || fail "check-config must validate the rendered app.yaml.new before it replaces app.yaml"
[[ ! -e "${deploy}/config/app.yaml.new" ]] || fail "app.yaml.new must be renamed into place"

# 不带对象存储参数：没有 object_store 段，也不要求那个凭据文件；残留的凭据被删掉。
grep -q 'object_store' "${config}" && fail "object_store must not be rendered without --object-store-endpoint"
printf 'ACCESS_KEY_ID=AKIDold\nSECRET_ACCESS_KEY=old\n' >"${secrets}/archive-object-store.env"
chmod 0600 "${secrets}/archive-object-store.env"
bash "${SCRIPT}" "${args[@]}" >"${tmp}/plain.log" 2>&1 || { cat "${tmp}/plain.log" >&2; fail "deploy without object store failed"; }
[[ ! -e "${secrets}/archive-object-store.env" ]] || fail "a leftover object store credential must be removed when the object store is off"

os_args=(--object-store-endpoint cos.ap-nanjing.myqcloud.com --object-store-region ap-nanjing --object-store-bucket moox-archive-1250000000
  --object-store-url-style virtual --object-store-storage-class STANDARD_IA)
# 带对象存储参数但缺凭据文件：失败。
if bash "${SCRIPT}" "${args[@]}" "${os_args[@]}" >"${tmp}/bad4.log" 2>&1; then
  fail "the object store credential must be required when the object store is on"
fi
grep -q 'archive-object-store.env' "${tmp}/bad4.log" || fail "the missing credential must be named"
printf 'ACCESS_KEY_ID=AKIDnew\nSECRET_ACCESS_KEY=new\n' >"${secrets}/archive-object-store.env"
chmod 0600 "${secrets}/archive-object-store.env"
# url_style 非法：失败。
if bash "${SCRIPT}" "${args[@]}" --object-store-endpoint cos.ap-nanjing.myqcloud.com --object-store-region ap-nanjing \
  --object-store-bucket moox-archive-1250000000 --object-store-url-style auto >"${tmp}/bad5.log" 2>&1; then
  fail "an invalid --object-store-url-style must be rejected"
fi
if bash "${SCRIPT}" "${args[@]}" "${os_args[@]}" --object-store-prefix no-slash >"${tmp}/bad6.log" 2>&1; then
  fail "a prefix without a trailing slash must be rejected"
fi
bash "${SCRIPT}" "${args[@]}" "${os_args[@]}" >"${tmp}/os.log" 2>&1 || { cat "${tmp}/os.log" >&2; fail "deploy with object store failed"; }
for want in 'object_store:' 'endpoint: "cos.ap-nanjing.myqcloud.com"' 'region: "ap-nanjing"' 'bucket: "moox-archive-1250000000"' 'prefix: "moox-archive/"' \
  'url_style: virtual' 'use_tls: true' 'storage_class: STANDARD_IA' "credentials_file: ${secrets}/archive-object-store.env"; do
  grep -qF -- "${want}" "${config}" || fail "rendered object_store misses: ${want}"
done
grep -q 'AKIDnew\|SECRET_ACCESS_KEY' "${config}" && fail "the rendered config must not contain the object store credential"

# check-config 不通过：脚本失败，app.yaml 与已安装的二进制保持原样，服务没有被重启。
before_config="$(cat "${config}")"
before_binary="$(cat "${deploy}/bin/moox-archive-worker")"
mkdir -p "${tmp}/fakebin"
printf '#!/bin/sh\necho "$@" >>"%s/systemctl.log"\n' "${tmp}" >"${tmp}/fakebin/systemctl"
chmod 0755 "${tmp}/fakebin/systemctl"
start_args=()
for arg in "${args[@]}"; do [[ "${arg}" == --no-start ]] || start_args+=("${arg}"); done
if PATH="${tmp}/fakebin:${PATH}" bash "${SCRIPT}" "${start_args[@]}" --binary "${tmp}/bad-worker" --export-format csv >"${tmp}/bad7.log" 2>&1; then
  fail "a config that fails check-config must stop the deployment"
fi
grep -q 'check-config' "${tmp}/bad7.log" || fail "the failure must mention check-config"
[[ "$(cat "${config}")" == "${before_config}" ]] || fail "app.yaml must stay unchanged when check-config fails"
[[ "$(cat "${deploy}/bin/moox-archive-worker")" == "${before_binary}" ]] || fail "the installed binary must stay unchanged when check-config fails"
[[ ! -e "${deploy}/config/app.yaml.new" && ! -e "${deploy}/bin/moox-archive-worker.new" ]] || fail "the rejected config and binary must be cleaned up"
[[ ! -e "${tmp}/systemctl.log" ]] || fail "the service must not be restarted when check-config fails"

# ---------------------------------------------------------------- 内置对象存储（SeaweedFS）
# 桩：systemctl 记日志并按状态文件回答 is-active；ss 按标记文件回答端口占用；timeout 直接执行；weed 模拟 shell 的输出。
sw_state="${tmp}/sw-state"
mkdir -p "${sw_state}" "${tmp}/swbin" "${tmp}/pkg"
cat >"${tmp}/swbin/systemctl" <<EOF
#!/bin/sh
echo "\$@" >>"${tmp}/sw-systemctl.log"
case "\$*" in
  *is-active*) [ -e "${sw_state}/active" ] ;;
  *restart*|*" start "*) touch "${sw_state}/active"; touch "${sw_state}/port-11473" ;;
  *disable*) rm -f "${sw_state}/active" ;;
esac
exit \${?}
EOF
cat >"${tmp}/swbin/ss" <<EOF
#!/bin/sh
port="\$(printf '%s' "\$*" | sed -n 's/.*sport = :\([0-9]*\).*/\1/p')"
[ -e "${sw_state}/port-\${port}" ] && echo "LISTEN 0 4096 127.0.0.1:\${port} 0.0.0.0:*"
exit 0
EOF
printf '#!/bin/sh\nshift\nexec "$@"\n' >"${tmp}/swbin/timeout"
cat >"${tmp}/pkg/weed" <<EOF
#!/bin/sh
cmd="\$(cat)"
echo "\${cmd}" >>"${tmp}/sw-weed.log"
case "\${cmd}" in
  cluster.check) echo "checking master 127.0.0.1:11470 to volume server 127.0.0.1:11471.21471 ... ok round trip 0.4ms" ;;
  s3.bucket.list) [ -e "${sw_state}/bucket" ] && printf '  moox-archive\tsize:0\n' ;;
  s3.bucket.create*) if [ -e "${sw_state}/bucket" ]; then echo "error: bucket moox-archive already exists"; else touch "${sw_state}/bucket"; fi ;;
esac
exit 0
EOF
cp "${tmp}/moox-archive-worker" "${tmp}/pkg/moox-archive-worker"
chmod 0755 "${tmp}/swbin/"* "${tmp}/pkg/"*
sw_dir="${tmp}/seaweedfs"
sw_unit="${MOOX_SYSTEMD_USER_DIR}/moox-seaweedfs.service"
mode_of() { stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"; }
b_args=()
for arg in "${args[@]}"; do [[ "${arg}" == "${tmp}/moox-archive-worker" ]] && b_args+=("${tmp}/pkg/moox-archive-worker") || b_args+=("${arg}"); done
b_args+=(--builtin-object-store)

# 与外部对象存储互斥。
if PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" "${b_args[@]}" "${os_args[@]}" >"${tmp}/sw-bad1.log" 2>&1; then
  fail "--builtin-object-store and --object-store-endpoint must be mutually exclusive"
fi
# 包里没有 weed：失败，worker 配置不变。
mkdir -p "${tmp}/pkg-noweed" && cp "${tmp}/moox-archive-worker" "${tmp}/pkg-noweed/moox-archive-worker"
if PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" "${args[@]}" --binary "${tmp}/pkg-noweed/moox-archive-worker" --builtin-object-store >"${tmp}/sw-bad2.log" 2>&1; then
  fail "the built-in object store must require weed next to --binary"
fi
# 端口被别的程序占用：失败。
touch "${sw_state}/port-11471"
if PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" "${b_args[@]}" >"${tmp}/sw-bad3.log" 2>&1; then
  fail "a busy port must stop the built-in object store"
fi
grep -q 'port 11471' "${tmp}/sw-bad3.log" || fail "the busy port must be named"
grep -qF 'cos.ap-nanjing.myqcloud.com' "${config}" || fail "the worker config must stay unchanged when the built-in object store fails"
grep -qF 'AKIDnew' "${secrets}/archive-object-store.env" || fail "the worker credential must stay unchanged when the built-in object store fails"
[[ ! -e "${secrets}/archive-object-store.env.new" && ! -e "${deploy}/config/app.yaml.new.check" ]] || fail "rejected candidates must be cleaned up"
rm -f "${sw_state}/port-11471"

# 首次部署：生成密钥、派生 s3.json 与 worker 凭据（都 0600），--no-start 也启动 SeaweedFS 并建桶。
: >"${tmp}/sw-systemctl.log"
PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" "${b_args[@]}" >"${tmp}/sw-ok.log" 2>&1 || { cat "${tmp}/sw-ok.log" >&2; fail "deploy with the built-in object store failed"; }
keys="${sw_dir}/config/s3-credentials.env"
for f in "${keys}" "${sw_dir}/config/s3.json" "${secrets}/archive-object-store.env"; do
  [[ -s "${f}" && "$(mode_of "${f}")" == 600 ]] || fail "${f} must exist with mode 0600"
done
[[ -x "${sw_dir}/bin/weed" && -s "${sw_dir}/LICENSE" ]] || fail "weed and its license must be installed"
[[ "$(mode_of "${sw_dir}")" == 700 ]] || fail "the SeaweedFS home must be private (0700) like the other unit directories"
[[ "$(tail -n 1 "${tmp}/check-config.log")" == "${deploy}/config/app.yaml.new.check" ]] || fail "check-config must validate a copy that points at the new credential"
[[ ! -e "${deploy}/config/app.yaml.new.check" && ! -e "${secrets}/archive-object-store.env.new" ]] || fail "the check copy and the new credential must be cleaned up"
cmp -s "${keys}" "${secrets}/archive-object-store.env" || fail "the worker credential must be derived from the built-in key"
access="$(sed -n 's/^ACCESS_KEY_ID=//p' "${keys}")"; secret="$(sed -n 's/^SECRET_ACCESS_KEY=//p' "${keys}")"
[[ "${access}" =~ ^MOOX[0-9A-F]{16}$ && "${secret}" =~ ^[0-9a-f]{40}$ ]] || fail "unexpected generated key format"
grep -qF "\"accessKey\": \"${access}\"" "${sw_dir}/config/s3.json" && grep -qF "\"secretKey\": \"${secret}\"" "${sw_dir}/config/s3.json" || fail "s3.json must carry the built-in key"
grep -qF 'arn:aws:s3:::moox-archive/moox-archive/*' "${sw_dir}/config/s3.json" || fail "the policy must be limited to the prefix"
grep -q 'DeleteObject\|"Write"' "${sw_dir}/config/s3.json" && fail "the policy must not allow deletes"
python3 -I -c 'import json,sys; c=json.load(open(sys.argv[1])); json.loads(c["policies"][0]["content"])' "${sw_dir}/config/s3.json" || fail "s3.json and its policy must be valid JSON"
grep -q "${access}\|${secret}" "${tmp}/sw-ok.log" && fail "the generated key must not be printed"
for want in 'endpoint: "127.0.0.1:11473"' 'region: "us-east-1"' 'bucket: "moox-archive"' 'prefix: "moox-archive/"' 'url_style: path' 'use_tls: false' \
  "credentials_file: ${secrets}/archive-object-store.env"; do
  grep -qF -- "${want}" "${config}" || fail "built-in object_store misses: ${want}"
done
grep -q 'storage_class' "${config}" && fail "the built-in object store has no storage class"
[[ -s "${sw_unit}" ]] || fail "moox-seaweedfs.service was not rendered"
grep -q '__[A-Z_]*__' "${sw_unit}" && fail "unrendered placeholder in moox-seaweedfs.service"
for want in "-dir=${sw_dir}/data" '-ip.bind=127.0.0.1' '-master.telemetry=false' '-s3.port=11473' '-s3.port.iceberg=0' '-s3.port.lance=0' "-s3.config=${sw_dir}/config/s3.json"; do
  grep -qF -- "${want}" "${sw_unit}" || fail "moox-seaweedfs.service misses: ${want}"
done
grep -q 'restart moox-seaweedfs.service' "${tmp}/sw-systemctl.log" || fail "a fresh install must (re)start SeaweedFS"
grep -q 'moox-archive-worker' "${tmp}/sw-systemctl.log" && fail "--no-start must not touch the worker service"
[[ -e "${sw_state}/bucket" ]] || fail "the bucket must be created"

: >"${tmp}/sw-weed.log"
# 二次部署（--skip-build）：沿用已装的 weed 与密钥，重启 SeaweedFS，桶已存在不重建。
: >"${tmp}/sw-systemctl.log"
before_keys="$(cat "${keys}")"
PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" --dir "${deploy}" --root-dir "${archive_root}" --worker-id archive-worker@testhost \
  --access-address access.example.test:11004 --access-id access@storage --access-key-id key-123 --skip-build --no-start --builtin-object-store \
  >"${tmp}/sw-ok2.log" 2>&1 || { cat "${tmp}/sw-ok2.log" >&2; fail "second built-in deploy failed"; }
[[ "$(cat "${keys}")" == "${before_keys}" ]] || fail "the built-in key must be kept across deployments"
grep -q 'restart moox-seaweedfs.service' "${tmp}/sw-systemctl.log" || fail "every deployment must restart SeaweedFS"
grep -q 's3.bucket.create' "${tmp}/sw-weed.log" && fail "an existing bucket must not be created again"

# 改用外部对象存储：停掉 SeaweedFS，数据与密钥留着。
: >"${tmp}/sw-systemctl.log"
printf 'ACCESS_KEY_ID=AKIDnew\nSECRET_ACCESS_KEY=new\n' >"${secrets}/archive-object-store.env"
chmod 0600 "${secrets}/archive-object-store.env"
PATH="${tmp}/swbin:${PATH}" bash "${SCRIPT}" "${args[@]}" "${os_args[@]}" >"${tmp}/sw-ext.log" 2>&1 || { cat "${tmp}/sw-ext.log" >&2; fail "switching to an external object store failed"; }
grep -q 'disable --now moox-seaweedfs.service' "${tmp}/sw-systemctl.log" || fail "switching to an external object store must stop SeaweedFS"
[[ -d "${sw_dir}/data" && "$(cat "${keys}")" == "${before_keys}" ]] || fail "SeaweedFS data and key must stay"

echo "deploy-archive-worker contract passed"
