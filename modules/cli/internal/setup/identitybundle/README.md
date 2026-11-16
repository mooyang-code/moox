# 主机身份包消费

`identitybundle` 接收控制机上 `moox-admin-cli` 签发的身份包，供 `modules/cli/internal/setup/deploy` 在部署时使用；也管这台操作电脑自己的身份文件。公开清单类型由 `packages/servicecatalog/hostbundle` 提供，生产端和消费端共用。

身份包的内容：主机签名私钥 `host.key`（只在这次新签发时有）、主机证书 `host.crt` 与 `host.tls.key`、根证书 `moox-ca.crt`；部署了 collector 的主机（控制机）另有 `scf-collector.key`（新签发时）。

调用方提供当前登记拓扑的主机 ID、控制主机 ID、公开/私网地址、是否只能出站、是否部署了 collector，以及已确认的 CA 指纹。首次信任通过已核验主机指纹的 control SSH 建立，后续使用已保存的 CA 指纹；不得从未验证载荷中自行选择信任根。库不读取 `moox.toml`，不创建 CA 或任何主机身份。

- `Fetch` 接收可信 SSH 返回的公开清单和该客户端的 `Download` 方法，把身份包取回内存（私钥不落到操作电脑的磁盘）。先核对远端 `bundle.json` 与 Admin 响应，再按声明长度限制每次下载，校验逐文件 SHA256。单文件上限 1 MiB、总量 16 MiB。
- 校验内容：清单只能列出上面那几个文件（多一个都拒绝，CA 私钥和 Admin 主密钥不能成为载荷）；私钥对应清单里登记的公钥；`scf-collector` 的公钥只属于部署了 collector 的主机；CA/主机证书的元数据、有效期、签发链、精确 SAN 和私钥配对；只能出站的主机证书必须带 ClientAuth，直达主机不能带。
- `FetchEventBusClients` 只取清单里声明的 EventBus 角色文件与公开 CA；EventBus 所在主机另带服务端证书、私钥和用户表。
- 操作电脑的身份（`~/.config/moox`）：`EnsureSigningKeyFile` 生成 `moox-cli.key`（已有就原样复用，文件坏了不悄悄换新的）；`InstallOperatorTrust` 把已校验的控制机根证书与它的指纹记下来（`moox-ca.crt`、`gateway-bootstrap.json`），已记着另一套控制机 CA 时拒绝覆盖（`ReadOperatorPin` 读已保存的指纹）；`RemoveOperatorTrust` 在全新部署控制机前清掉指纹与根证书，`RemoveOperatorIdentity` 在清空全部主机后连私钥一起删。
