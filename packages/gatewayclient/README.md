# 网关客户端

`Invoke` 将对象按配置的 PB 或 JSON 编码后签名；`Forward` 对传入的 PB/JSON 字节签名并原样发送。调用权限、请求与响应大小、默认超时以及只读方法均取自 `servicecatalog`。

内部组件可用 `FileConfig` 读取 `gateway_client: { caller, key_file, host_id, local_address, ca_file }`，再通过 `OpenInternal` 创建客户端。`caller` 是组件 ID；
`key_file` 是所在主机的 ed25519 签名私钥（`<部署根>/secrets/host.key`，本机全部组件共用，内容是一行 base64 的 32 字节种子），必须是 0600 的普通文件，拒绝符号链接。
`host_id`、`local_address`（本机网关 `127.0.0.1:11002`）、`ca_file`（MooX CA 根证书）由部署渲染进每个组件的配置；签名时带上签名主机 `host_id`，
目标网关用这台主机登记的公钥验签，并核对这个组件确实部署在这台主机上。目录缓存写入调用方提供的数据目录下的 `gatewayclient/directory.json`。
`Validate` 只检查公开字段，离线命令可以在没有主机 ID 时载入配置；`OpenInternal` 要求完整。客户端由进程或命令持有并在退出时关闭。
因子引擎、归档 worker 也走这个入口：它们是普通调用方，用因子机的主机密钥直连存储机与控制机的主机网关。

外部身份（`scf-collector`、`collector-operator`、`moox-skill`）使用 `ExternalFileConfig`，再调用 `OpenExternal(配置文件路径)`；`key_file` 是这个身份自己的私钥，相对配置文件解析，也支持绝对路径。
逐方法权限取自组件目录。

```yaml
gateway_client:
  access_address: access.example.test:11004
  access_id: access@storage
  caller: moox-skill
  key_file: .moox-skill-keys/moox-skill.key
```

此入口复用内部客户端的私密文件校验，但不读取主机配置、目录缓存、SSH 或环境凭据；密钥文件缺失或无效即失败。客户端由外部进程持有并关闭。SCF 无需配置文件，由函数入口显式读取部署环境里的私钥，
再通过 `New(Config{Mode: External, ...})` 传入固定 Access 与签名身份；共享包不会查找环境变量。外部请求的签名目标始终是 `access@<主机>`，不带签名主机；禁止的方法在发送前拒绝。

网关（或外部入口）自己拒签时返回专用错误码 `gatewayauth.RetRejected`：验签、时间窗、防重放、调用名单任何一步不过都是它，请求没有到达服务。客户端收到后刷新目录、
重新签名重发一次，对写方法也一样；重签后仍被拒就报错。服务自己返回的鉴权类错误不重发。

调用方完成身份认证及空间授权后，可用 `WithCallMetadata` 传入可信的用户 ID、空间 ID 和 trace ID，客户端将非空值作为 `X-User-Id`、`X-Space-Id`、`X-Trace-Id` 三个 tRPC 元数据字段发送。此接口不能修改网关签名身份，也不加工请求 JSON。Admin 控制台以 `console` 的身份、用控制机的主机密钥签名，目录来源直接读取 Admin 拓扑表，避免首次启动依赖尚未启动的本机网关。

`Config.Mode` 支持三种方式：

- `local`（`Internal`）：从本机 `127.0.0.1:11002` 取得目录，优先调用本机服务；跨主机连接目标的 TLS `11003`，只信任 `CAFile` 指定的 MooX 私有 CA，并使用主机 ID 校验证书名称。
- `access`（`External`）：使用固定的 `AccessAddress` 与 `access@<主机>` 实例 ID，不获取目录；按照外部调用方的逐方法白名单发送签名请求。
- `tunnel`（`Tunnel`）：供 `moox-cli` 使用，调用方提供目录来源与 SSH 隧道管理器；解析结果必须是字面 IP 的 loopback 地址。

内部与隧道客户端构造时取得有效目录，失败时可以使用已校验的落盘缓存；两者均不可用则启动失败。目录默认每 5 秒刷新；网络错误、服务不存在或网关拒签时立即刷新（外部客户端没有目录）。

一次调用最多尝试两次，每次重新选路并生成 nonce，重试、刷新和传输共用超时预算。获取连接失败且请求尚未发出时，所有方法都可重试；请求可能已发出时，只有目录声明的只读方法和幂等写方法可在网络错误、超时等情况下重试，当前幂等写名单只有 `PrimaryStore.CommitTimeSeriesBatch`。建连失败与幂等写重试都要求至少剩余 1.5 秒；普通业务错误不重试。调用方可用 `WithoutRetry(ctx)` 关闭包内重试。

缓存以内容哈希校验，限制为 4 MiB，临时文件使用 `0600` 权限并通过 rename 替换。刷新得到的撤回先应用到内存，落盘失败会报告错误，不会重新启用被撤回的主机。`Directory()` 返回独立副本。

每个客户端持有自己的连接池。tRPC SDK 的默认池不区分 CA/SNI，本包按 TLS 身份隔离池，并保留同一身份的长连接。CA 配置在客户端生命周期内固定；修改 CA 后须重建客户端。`Close` 停止刷新并关闭持有的连接。`NewLocalDirectorySource` 创建的来源有独立的 `Close`；由调用方传入的来源及隧道由调用方管理生命周期。自定义目录来源必须遵守 context 取消，刷新错误回调应及时返回。

主机网关、Access 与业务调用方已接入本包；实际运行入口见各模块的 `cmd/`。本包离线测试不代表多主机网络或生产部署已经验收。
