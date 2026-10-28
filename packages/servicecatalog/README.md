# 组件目录与权限

`catalog.yaml` 内嵌定义组件、公开 tRPC 服务、逐方法 ACL、外部调用方白名单和 Doctor 元数据。当前有 21 个组件、5 个外部调用方；组件数不等于主机数或进程副本数。目录不包含部署地址、签名密钥或其他凭据。

`LoadEmbedded` / `Decode` 严格加载并校验目录。`ValidateTopology` 整体校验主机与部署的端口、范围、副本数、受保护组件和自动维护的主机组件，不修改输入。`Compile` 生成指定主机的 loopback 路由、全局服务目录和校验密钥的调用方范围，输出与输入顺序无关。

- 控制台使用 `ConsoleService(name, method)`，再以 `console` 身份校验。Storage 的三个服务共用 `storage` 名称，通过方法区分；同名同方法的歧义被拒绝。
- `Allowed` 供控制台与主机网关共用；外部接入使用 `PrincipalAllowed`。外部调用方身份不能直接进入主机网关，白名单涉及的方法会自动授予 `access`。
- `ReadOnly` 识别只读方法；`Retryable` 还包含 `idempotent_methods` 中的幂等写方法，当前是 `PrimaryStore.CommitTimeSeriesBatch`。是否实际重试还受客户端错误分类与剩余预算约束。
- 路由按单个 service path / method 编译，保留原 ACL 方法与调用方的对应关系。
- `host-gateway@*` 只允许用于 GatewayControl，编译时展开为已登记主机；控制服务仍须校验调用方主机与请求中的主机一致。
- `CompiledHost.Hash` 是路由定义哈希。GatewayControl 应把实际下发的校验密钥及 KeyID 加入最终快照哈希，保证密钥轮换能触发更新。

SysDeploy v2、GatewayControl、调用方密钥管理、原生初始化、Doctor、主机网关、Access 与业务调用方均已使用此目录；出口代理已有运行入口和服务实现。目录是源码契约，不能单独证明某个部署环境已运行或通过验收。

`hostgatewayconfig` 子包定义原生部署与主机网关运行端共用的配置格式。`Default` 生成发布目录中的相对路径和 control 的直连配置；`Decode` 严格校验有界单份 YAML，`Load` 另将路径解析为相对于配置文件的绝对路径。该层只校验结构，实际 TLS 证书、私有 CA 与签名文件由运行端检查。安装器、网关运行端与内部调用方均已接入。
