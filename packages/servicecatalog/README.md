# 组件目录与权限

`catalog.yaml` 内嵌定义组件、公开 tRPC 服务、逐方法的调用权限、外部身份和 Doctor 元数据。当前有 20 个组件、5 个外部身份；组件数不等于主机数或进程副本数。目录不包含部署地址、签名密钥或其他凭据。

目录是 `version: 2` 的结构：每个方法只写一遍，属性跟在方法上。

- `read_only`、`idempotent`、`timeout_ms`（覆盖服务级超时）写在方法上。
- `callers` 是能调它的机器身份：组件 ID、`identities` 里的外部身份；`host-gateway` 指每台主机的网关，只用于 GatewayControl。省略表示没有机器能调。
- `operators` 收窄人操作的 `console` 与 `moox-cli`：省略表示两者都能调，写 `[]` 表示都不能调，写其中一个表示只给它。
- `identities` 列出外部身份（`external: true`），它们只能经 Access 进入；与外部程序组件（因子引擎、归档 worker）同名的身份就是这个程序访问系统用的身份。

`LoadEmbedded` / `Decode` 严格加载并校验目录。`ValidateTopology` 整体校验主机与部署的端口、范围、副本数、受保护组件和自动维护的主机组件，不修改输入。`Compile` 生成指定主机的 loopback 路由、全局服务目录和校验密钥的调用方范围，输出与输入顺序无关。

- 控制台使用 `ConsoleService(name, method)`，再以 `console` 身份校验。Storage 的三个服务共用 `storage` 名称，通过方法区分；同名同方法的歧义被拒绝。
- `Allowed` 供控制台与主机网关共用；外部接入使用 `PrincipalAllowed`。外部身份不能直接进入主机网关，它们能调的方法自动授予 `access`。
- `ReadOnly` 识别只读方法；`Retryable` 还包含标了 `idempotent` 的幂等写方法，当前是 `PrimaryStore.CommitTimeSeriesBatch`。是否实际重试还受客户端错误分类与剩余预算约束。
- 路由按单个 service path / method 编译，保留方法与调用方的对应关系。
- `host-gateway` 是每台主机网关的调用方名字（用所在主机的密钥签名，签名主机另由签名头给出）；控制服务仍须校验签名主机与请求中的主机一致。`SigningCallers` 给出每台主机上可以用主机密钥签名的调用方。
- `CompiledHost.Hash` 是路由定义哈希。GatewayControl 把实际下发的校验公钥一起算进最终快照哈希，换密钥就会触发更新。

访问控制基线：`testdata/acl-baseline.json`（每个调用方、外部身份能调的方法）与 `testdata/compile-baseline.json`（固定拓扑下每台主机的路由编译结果），`baseline_test.go` 每次测试逐项核对。改了目录或编译逻辑之后按 `baseline_gen_test.go` 文件头的用法重新生成，看过差异再提交。

SysDeploy v2、GatewayControl、调用方密钥管理、原生初始化、Doctor、主机网关、Access 与业务调用方均已使用此目录；出口代理已有运行入口和服务实现。目录是源码契约，不能单独证明某个部署环境已运行或通过验收。

`hostgatewayconfig` 子包定义原生部署与主机网关运行端共用的配置格式。`Default` 生成发布目录中的相对路径和 control 的直连配置；`Decode` 严格校验有界单份 YAML，`Load` 另将路径解析为相对于配置文件的绝对路径。该层只校验结构，实际 TLS 证书、私有 CA 与签名文件由运行端检查。安装器、网关运行端与内部调用方均已接入。
