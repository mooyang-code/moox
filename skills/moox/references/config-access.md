# 读取与修改 moox.toml（加密文件）

`moox.toml` 默认是加密保存的（整份 AES-256-GCM，密钥在本机钥匙串，文件里只有信封）。
**Agent 不能 `cat`、`grep`、用 Python 解析它，也不要 `source` 或复制它**，一律通过 `moox-cli config` 的子命令。
下面的命令都在放着 `moox.toml` 的目录（主仓库根目录）里执行，或加 `--file <路径>`。
主仓库的工作副本要和 main 同步：改写命令会按 `factors.source_dir` 去因子库里核对因子，副本落后会报“因子库里没有因子 X”。

## 读：`moox-cli config get`

```bash
moox-cli config get                                   # 整份配置（JSON，密钥已打码）
moox-cli config get hosts.storage                     # 一个表
moox-cli config get storage_retention.defaults.5m     # 一个值
moox-cli config get factors.tasks.0                   # 数组用序号，从 0 开始
moox-cli config get hosts.storage.address
```

- 路径用点号分隔；路径不存在时报错并列出该层可用的键。
- 键名含 `password`、`passphrase`、`secret`、`token`、`api_key`、`access_key`、`private_key` 的值显示为 `***`（以 `_file`、`_path` 结尾的路径类键不打码），包括内嵌表里的，例如 `ssh = { password = "…" }`。
- `--reveal` 输出明文。**只有任务确实需要这个值才用**（例如用管理员账号登录管理台做页面检查），并且：只把值放进环境变量或标准输入传给下一条命令，不要打印到对话、日志、文档、提交里。要登录管理台时读 `admin.username` 与 `admin.password` 两项即可，不要取整份。
- 需要的只是“某项有没有配、是什么模式”时，用不加 `--reveal` 的结果就够了。

## 改：`moox-cli config set`

```bash
moox-cli config set hosts.storage.label=存储主机          # 只预览，不写回
moox-cli config set hosts.storage.label=存储主机 --yes     # 校验通过后写回
```

- 写法 `表名.键=值`，根部的键直接写键名。值按 `true`/`false`、整数、小数识别，其余按字符串。
- 只改**已有的单值键**，只动那一行，注释与其余内容原样保留；表或键不存在、表是 `[[数组表]]`、键的现值是数组或多行值，都会报错，不会凭空创建。
- 改后先按当前格式校验，通过且带 `--yes` 才写回：原文件备份为 `moox.toml.bak-<时间>`，新内容重新加密。预览与写回的输出里都没有配置值。
- 写回后提醒用户跑一次 `moox-cli config backup`（用管理员密码加密备份到管理台；Agent 不代跑）。
- 改的是本机文件；要让线上生效，还得按各项的说明重装或重启对应单元（见下表）。

## 专用改写：`moox-cli config migrate`

`set` 管不了的结构性改动用 `migrate` 的专用参数；默认只预览，加 `--yes` 写回，写回规则同上。

| 参数 | 作用 |
|---|---|
| `--set-retention defaults.5m=240d` 或 `<空间>.<频率>=<时长>` | 改保留期（目标表必须已存在）；**生效要重装 Storage 单元** |
| `--set-factors-enabled true|false` | `[factors] enabled` |
| `--set-reachability 主机ID=direct|outbound` | 主机可达性 |
| `--set-tls-mode 主机ID=auto|public|internal` | 控制台证书模式 |
| `--add-deployment 主机ID=组件ID[,组件ID]` | 把组件并入主机的 `[deployments]` |
| `--rename-host 旧ID=新ID` | 主机改名（主机表、`[deployments]`、引用一起改） |
| `--import-scf <JSON>` | 整体替换 `[scf_plan]` |

不带任何参数的 `migrate` 把旧格式改成当前格式（因子定义、主机中文名、旧组件名）。

## 其余情况

- 数组、数组表、大段结构（例如 `[[factors.tasks]]`）：让用户跑 `moox-cli config edit` 在本机网页里改，Agent 不填也不读。
- 手写的明文 `moox.toml`：`moox-cli config encrypt` 加密。
- 新电脑：`moox-cli config restore` 从管理台恢复。
- `config get` 报“这台电脑上没有解密 moox.toml 的密钥”：文件来自另一台电脑或钥匙串条目被删，用 `config restore`；不要重新生成密钥覆盖。

## 例子：把 5m 保留期调到 240 天

```bash
moox-cli config get storage_retention.defaults.5m                         # 先看现值
moox-cli config migrate --set-retention defaults.5m=240d                  # 预览
moox-cli config migrate --set-retention defaults.5m=240d --yes            # 写回
```

之后重装 Storage 单元让保留期生效；线上已经手工改过策略文件时，要让两边一致，否则下次重装会改回去。
