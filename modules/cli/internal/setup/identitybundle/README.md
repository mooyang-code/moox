# 主机身份包消费

`identitybundle` 接收控制机上 `moox-admin-cli` 导出的身份包，供 `modules/cli/internal/setup/deploy` 在部署时使用。公开清单类型由 `packages/servicecatalog/hostbundle` 提供，生产端和消费端共用；私密载荷保留在不导出字段的 `Material` 中，fmt/JSON 不输出密钥。

调用方提供当前登记拓扑的主机 ID、控制主机 ID、公开/私网地址、完整组件部署，以及已确认的 CA DER 指纹和期望快照摘要。首次信任通过已核验主机指纹的 control SSH 建立，后续使用已保存的 CA 指纹；不得从未验证载荷中自行选择信任根。库不读取 `moox.toml`，不创建 CA 或调用方身份。

- `Fetch` 接收可信 SSH 返回的公开元数据和该客户端的 `Download` 方法。先核对远端 `bundle.json` 与 Admin 响应，再按声明长度限制每次下载，校验逐文件 SHA256，完成语义验证后原子发布到新目录。单文件上限 1 MiB、总量 16 MiB；已有文件、目录或链接都不覆盖，失败清理暂存目录。
- 验证限定目标主机及已部署组件的签名身份，校验主机网关配置、CA/叶证书元数据、有效期、签发链、精确 SAN 和私钥配对。Access 只接收目录中的外部 principal 及其轮换重叠期密钥，KeyID 全局唯一；CA 私钥和 Admin 主密钥不能成为载荷。
- `FetchEventBusClients` 只取清单里声明的 EventBus 角色文件与公开 CA；EventBus 所在主机另带服务端证书、私钥和用户表。
- `InstallOperator` 只从控制机的身份包下载操作员身份和公开 CA，装到操作电脑的 `~/.config/moox`；先写密钥再切换配置，中断后可以重跑。已装过另一套控制机 CA 时拒绝覆盖（`ReadOperatorPin` 读已保存的指纹）。

`Fetch` 发布后父目录同步失败时，会同时返回非空 Material 和错误。`Fetch` 验证内容与传输边界；远端权限由 Admin 生产端保证，下载后本地权限仍强制为私密模式。

```bash
make test-host-identity-bundles
```
