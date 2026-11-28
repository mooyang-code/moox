# 读取与修改 moox.toml 与 moox.secrets.toml

部署配置是同一目录里的两份文件：`moox.toml` 是明文，只有拓扑与部署参数，不含秘密；
`moox.secrets.toml` 只保存部署启动所需的秘密（管理员、腾讯云密钥、CLS 写入密钥），整份 AES-256-GCM 加密，密钥在本机钥匙串，文件里只有信封。交易所密钥与告警通知渠道不写进秘密文件，安装完成后在管理台录入，清空重装时通过 `moox.backup` 备份与恢复。
**Agent 不能 `cat`、`grep`、用 Python 解析秘密文件，也不要 `source` 或复制它**，一律通过 `moox-cli config` 的子命令，用 `--file ./moox.secrets.toml` 指定它。
下面的命令都在放着这两份文件的目录（主仓库根目录）里执行，或加 `--file <路径>`。

## 读：`moox-cli config get`

```bash
moox-cli config get                                   # 整份 moox.toml（JSON，明文文件里没有秘密）
moox-cli config get hosts.storage                     # 一个表
moox-cli config get storage_retention.defaults.5m     # 一个值
moox-cli config get deployments.control.0            # 数组用序号，从 0 开始
moox-cli config get --file ./moox.secrets.toml admin              # 管理员：username 可见，password 显示为 ***
moox-cli config get --file ./moox.secrets.toml tencent_cloud      # 腾讯云密钥：secret_id、secret_key 均显示为 ***
moox-cli config get hosts.storage.address
```

- 路径用点号分隔；路径不存在时报错并列出该层可用的键。
- 秘密文件默认整份打码：只有用来辨认账号的 `username` 可见，其余的值（管理员密码、腾讯云密钥、CLS 写入密钥）都显示为 `***`。空值不打码，看得出没填。
- `moox.toml` 是明文，正常不含秘密；万一写了，键名含 `password`、`passphrase`、`secret`、`token`、`api_key`、`access_key`、`private_key` 的值也显示为 `***`（以 `_file`、`_path` 结尾的路径类键不打码）。
- `--reveal` 输出明文。**只有任务确实需要这个值才用**（例如用管理员账号登录管理台做页面检查），并且：只把值放进环境变量或标准输入传给下一条命令，不要打印到对话、日志、文档、提交里。要登录管理台时读 `admin.username` 与 `admin.password` 两项即可，不要取整份。
- 需要的只是“某项有没有配、是什么模式”时，用不加 `--reveal` 的结果就够了。

## 改：`moox-cli config set`

```bash
moox-cli config set hosts.storage.label=存储主机          # 只预览，不写回
moox-cli config set hosts.storage.label=存储主机 --yes     # 校验通过后写回
```

- 写法 `表名.键=值`，根部的键直接写键名。值按 `true`/`false`、整数、小数识别，其余按字符串。
- 只改**已有的单值键**，只动那一行，注释与其余内容原样保留；表或键不存在、表是 `[[数组表]]`、键的现值是数组或多行值，都会报错，不会凭空创建。
- 改后先按这份文件的格式校验，通过且带 `--yes` 才写回：原文件备份为 `<文件名>.bak-<时间>`，秘密文件的新内容重新加密。预览与写回的输出里都没有配置值。
- 改的是本机文件；要让线上生效，还得按各项的说明重新部署对应主机或重启对应组件（见下表）。

## 结构性改动：直接编辑 `moox.toml`

`moox.toml` 是明文、不含秘密：数组（`[deployments]`、`http_domains`）、`[scf_plan]`、主机改名这类 `set` 管不了的改动，直接编辑这份文件，改完用 `config get` 核对，再按下表重新部署对应主机生效。因子任务不在配置文件里，用 `factor task apply --file` 导入。

## 其余情况

- 常用配置与秘密修改：让用户跑 `moox-cli config edit` 在本机网页里改，Agent 不填也不读。
- 交易所密钥与告警通知渠道已从 `moox.secrets.toml` 移除：不要往秘密文件里手写 `[[exchange_keys]]` 或 `[notification]`（解析时会忽略并提示不再使用）；请用户在管理台「系统设置 › 密钥管理」录入交易所密钥、在「资源与运维 › 监控告警」的“消息通知设置”录入告警渠道，备份与恢复走 `setup backup` / `moox.backup`。
- 手写的明文 `moox.secrets.toml`：`moox-cli config encrypt` 加密。
- 新电脑：从原电脑拷两份文件和解密密钥（系统钥匙串里的，或 `~/.config/moox/config.key`）。
- `config get` 报“这台电脑上没有解密秘密文件的密钥”：文件来自另一台电脑或钥匙串条目被删，把原电脑的解密密钥带过来；不要重新生成密钥覆盖。

## 不经录入页生成两份配置（Agent 已知全部取值时）

`config init` 是本机网页表单，Agent 填不了。全部取值已知时手工生成：

1. **`moox.toml`**：复制 `moox.toml.example`，改 `[hosts.*]`（地址、`private_address`、地域、SSH 用户与 `private_key_file`；只支持密钥登录）、`[deployments]`、`[storage_retention]`、`[scf_plan]`（云账号、地域的 VPC 与子网、各空间；某个空间的每函数标的数写在它自己的 `[[scf_plan.spaces]]` 里，如 A 股 `symbols_per_function = 16`）。
   `credential_secret_id` 填 `tencent-default`。因子任务不写在这里（默认任务在 `config/setup/factor-tasks.yaml`，自己的任务用 `factor task apply --file`）。文件权限 0600。
2. **`moox.secrets.toml`**：按 `moox.secrets.toml.example` 的结构写出明文文件（0600，`O_EXCL` 新建；取值只在写文件的脚本进程内，不打印），随即 `moox-cli config encrypt --file ./moox.toml` 加密。
   之后用 `moox-cli config get --file ./moox.secrets.toml` 核对（除 `admin.username` 外应全是 `***`）。
3. `moox-cli setup validate --file ./moox.toml`：每台 `valid`、`route: fresh`、`manual_steps` 为空（没开 linger 的主机会列在这里）。

## 例子：把 5m 保留期调到 240 天

```bash
moox-cli config get storage_retention.defaults.5m                         # 先看现值
moox-cli config set storage_retention.defaults.5m=240d                    # 预览
moox-cli config set storage_retention.defaults.5m=240d --yes              # 写回
```

之后重新部署存储机让保留期生效（策略文件每次部署都从 `moox.toml` 渲染）。
