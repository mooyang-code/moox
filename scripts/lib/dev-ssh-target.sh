# MooX 内部开发：解析 / 提示 SSH 目标（不写密码，不入库）
#
# Usage:
#   source "${ROOT}/scripts/lib/dev-ssh-target.sh"
#   moox_dev_load_local_env
#   moox_dev_resolve_ssh_target "${ROOT}" "${CLI_TARGET}" TARGET "${NON_INTERACTIVE}"

MOOX_DEV_ENV_FILE="${HOME}/.moox-dev.env"

moox_dev_load_local_env() {
  if [[ -f "${MOOX_DEV_ENV_FILE}" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "${MOOX_DEV_ENV_FILE}"
    set +a
  fi
}

moox_dev_ssh_from_infra_local() {
  local root="${1:-}"
  local local_yaml="${root}/infra/infra.local.yaml"
  local ssh_line target

  [[ -f "${local_yaml}" ]] || return 1
  ssh_line="$(grep -E '^\s*ssh:\s*' "${local_yaml}" | head -1 || true)"
  [[ -n "${ssh_line}" ]] || return 1
  target="$(echo "${ssh_line}" | sed -E 's/^\s*ssh:\s*"?([^"#]+)"?.*/\1/' | tr -d ' ')"
  [[ -n "${target}" && "${target}" != *"<"* ]] || return 1
  printf '%s' "${target}"
}

moox_dev_save_ssh_target() {
  local target="${1:-}"
  local choice line

  [[ -n "${target}" ]] || return 1
  if [[ ! -t 0 ]]; then
    return 0
  fi

  read -r -p "保存 SSH 目标到 ${MOOX_DEV_ENV_FILE} 供下次使用? [y/N]: " choice
  if [[ ! "${choice}" =~ ^[Yy]$ ]]; then
    return 0
  fi

  if [[ -f "${MOOX_DEV_ENV_FILE}" ]] && grep -qE '^MOOX_DEV_SSH_TARGET=' "${MOOX_DEV_ENV_FILE}"; then
    read -r -p "文件中已有 MOOX_DEV_SSH_TARGET，覆盖? [y/N]: " choice
    if [[ ! "${choice}" =~ ^[Yy]$ ]]; then
      return 0
    fi
    # 删除旧行，保留其余配置
    local tmp
    tmp="$(mktemp)"
    grep -vE '^MOOX_DEV_SSH_TARGET=' "${MOOX_DEV_ENV_FILE}" >"${tmp}" || true
    mv "${tmp}" "${MOOX_DEV_ENV_FILE}"
  fi

  if [[ ! -f "${MOOX_DEV_ENV_FILE}" ]]; then
    {
      echo "# MooX dev local config — do not commit (see skills/dev-helper/env.example)"
      echo "MOOX_DEV_SSH_TARGET=${target}"
    } >"${MOOX_DEV_ENV_FILE}"
  else
    echo "MOOX_DEV_SSH_TARGET=${target}" >>"${MOOX_DEV_ENV_FILE}"
  fi
  printf '[dev-ssh] 已写入 %s\n' "${MOOX_DEV_ENV_FILE}"
}

moox_dev_prompt_ssh_target() {
  local __var="${1:-TARGET}"
  local tag="${2:-dev-ssh}"

  if [[ ! -t 0 ]]; then
    return 1
  fi

  printf '\n[%s] 未配置 SSH 目标。\n' "${tag}"
  printf '  请使用 SSH 公钥登录；勿将密码写入仓库或脚本。\n'
  printf '  也可预先设置: MOOX_DEV_SSH_TARGET / infra/infra.local.yaml / %s\n\n' "${MOOX_DEV_ENV_FILE}"

  local input=""
  read -r -p "SSH 目标 (user@host 或 ~/.ssh/config 别名): " input
  input="$(echo "${input}" | xargs)"
  [[ -n "${input}" ]] || return 1
  printf -v "${__var}" '%s' "${input}"
  moox_dev_save_ssh_target "${input}"
  return 0
}

# 解析顺序: CLI > MOOX_DEV_SSH_TARGET > infra.local.yaml > 交互提示
moox_dev_resolve_ssh_target() {
  local root="${1:-}"
  local cli_target="${2:-}"
  local __var="${3:-TARGET}"
  local non_interactive="${4:-0}"
  local resolved=""

  if [[ -n "${cli_target}" ]]; then
    printf -v "${__var}" '%s' "${cli_target}"
    return 0
  fi

  if [[ -n "${MOOX_DEV_SSH_TARGET:-}" ]]; then
    printf -v "${__var}" '%s' "${MOOX_DEV_SSH_TARGET}"
    return 0
  fi

  resolved="$(moox_dev_ssh_from_infra_local "${root}" || true)"
  if [[ -n "${resolved}" ]]; then
    printf -v "${__var}" '%s' "${resolved}"
    return 0
  fi

  if [[ "${non_interactive}" == "1" ]]; then
    return 1
  fi

  moox_dev_prompt_ssh_target "${__var}"
}
