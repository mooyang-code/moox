# 主机身份包消费

`unitbundle` 接收离线 Admin CLI 发布的身份包，供部署流程使用。公开清单类型由 `packages/servicecatalog/hostbundle` 提供，生产端和消费端共用；私密载荷保留在不导出字段的 `Material` 中，fmt/JSON 不输出密钥。

调用方提供当前登记拓扑的主机 ID、控制主机 ID、公开/私网地址、完整组件放置，以及已确认的 CA DER 指纹和期望快照摘要。首次信任通过已核验主机指纹的 control SSH 建立，后续使用已保存的 CA 指纹；不得从未验证载荷中自行选择信任根。库不读取运维 `moox.toml`，不创建 CA 或调用方身份。

- `Load` 读取现有物理私密目录：目录 0700、文件 0600 且由当前用户持有。清单必须为生产端的规范 JSON；禁止未知/重复字段、多文档、越界/重复路径、链接和未声明载荷。
- `Fetch` 接收可信 SSH 返回的公开元数据和该客户端的 `Download` 方法。先核对远端 `bundle.json` 与 Admin 响应，再按声明长度限制每次下载，校验逐文件 SHA256，完成语义验证后原子发布到新目录。单文件上限 1 MiB、总量 16 MiB；已有文件、目录或链接都不覆盖，失败清理暂存目录。
- 验证限定目标主机及已放置组件的签名身份，校验主机网关配置、CA/叶证书元数据、有效期、签发链、精确 SAN 和私钥配对。Access 只接收目录中的外部 principal 及其轮换重叠期密钥，KeyID 全局唯一；CA 私钥和 Admin 主密钥不能成为载荷。
- `PublishServices` 从已验证材料生成服务目录，去掉操作员配置与密钥；保留主机身份及业务身份的规范相对路径和源清单信息。只有 control bootstrap 可以接收操作员身份。操作员本机安装和服务激活仍由部署器接线。

`Inject` 把已验证材料投影到调用方独占的 0700 准备目录：只写所选组件身份，host 单元独占主机 TLS，Access 单元独占外部验证表；console-proxy 同时取得 console 调用身份，操作员身份始终排除。只有来自身份包的 host-gateway 配置可显式替换软件模板，身份文件不覆盖已有对象。失败时调用方必须丢弃暂存目录；[发布准备层](../unitinstall/README.md) 已接入此约定及失败清理。

`Directory()` 返回已发布位置，`Metadata()` 返回可独立修改的公开清单副本。发布后父目录同步失败时，会同时返回非空 Material 和错误；调用方须识别已经发布的目录，再决定重试。`Fetch` 验证内容与传输边界；远端权限由可信 Admin 生产端保证，下载后本地权限仍强制为私密模式。

主机助手提供 `moox-runtime inspect-bundle`：使用 `--directory`、`--host-id`、`--control-host-id`、`--address`、可选 `--private-address`、`--control-address`、`--components`、`--ca-sha256` 和 `--snapshot-sha256`，验证后只输出公开元数据。control bootstrap 额外使用 `--bootstrap-operator`；普通包必须省略。

```bash
make test-host-identity-bundles

# 全部制品预先在本机 CGO_ENABLED=0 编译，Linux 只执行测试
MOOX_UNIT_BUNDLE_TEST_BINARY=/absolute/path/unitbundle.test \
MOOX_ADMIN_CLI_BINARY=/absolute/path/moox-admin-cli \
MOOX_RUNTIME_BINARY=/absolute/path/moox-runtime \
make test-unit-bundle-linux
```

Linux 门禁强制十组消费/身份投影场景全部通过且无跳过，并从隔离空库真实执行 Admin bootstrap、重跑和三主机导出，再由真实助手检查每份包。可用 `MOOX_HOST_MATERIAL_FIXTURE_ROOT` 指定全新私密测试目录以保留合成材料，供本机实际 SSH 下载测试；默认在退出时清理。它不启动部署组件，不替代五步 bootstrap 或正式部署验收。
