# 控制主机 bootstrap 编排

`moox-runtime bootstrap --request PATH` 在目标 Linux 主机执行。输入是原生 CLI 生成的规范 0600 JSON：共享 `hostbundle.Topology`、部署根，以及 host/control 两个软件包的生产者摘要、独立单元根、逐组件私密环境和配置覆盖文件。它不读取或接收操作员 `moox.toml`。两个单元根必须是部署根下独立的物理目录；control 放置必须包含 Admin 与 EventBus，并满足服务目录约束。

请求摘要绑定规范拓扑、实际环境、软件包摘要和覆盖文件内容。进入停机前核验两个软件包，覆盖内容在内存中固定，后续写入本次尝试的私密目录。流程始终持有主机维护锁，并先记录两个旧 current、原先运行的组件和尝试标识，再建立跨单元持久 bootstrap 标记。已有单元必须同时存在，空环境则同时为空。

已有 control 的 Admin 数据库必须是发布内非空、归属当前用户的 0600 `admin/data/admin.db`，并与其实际环境和配置路径一致；缺失或使用外部路径时在停机前拒绝，避免升级静默初始化另一组签名密钥。服务专用启动子进程在 exec 前设置 umask 0077，保证数据库及缓存后续创建的文件保持私密，协调进程的 umask 不变。

执行顺序：

1. 停止旧 control、host 的全部写入者，在独立目录复制关闭后的 Admin 数据库及 WAL；首次初始化保留持久离线数据库，失败重试继续使用同一组已生成的 CA/KeyID。
2. 从校验后的 control 软件执行真实 `moox-admin-cli bootstrap`；Admin 主密钥与 MooX CA 持久保存在部署根 `identity/`，不进入状态种子或发布。子进程继承独占维护锁，并设置 Linux 父进程死亡信号；创建子进程的 OS 线程保持存活直到 Wait 返回。
3. 在同一关闭的数据库上执行 EventBus `ensure`/`export`，按目录端点生成或复用角色及 TLS 身份，签发事务提交后才返回成功。准备层按组件投影角色文件，启用 broker 鉴权/TLS 并使用发布内副本。准备并以 `NoStart` 激活 host，建立 business 准备需要的 `host/current` 目录视图；准备 control，离线初始化或导入代理状态，复制并封存 Admin/代理状态，再以 `NoStart` 激活 control。文件与各级目录同步后才发布持久材料和授权凭据。
4. 依次等待 Admin ready、HostGateway 的快照/心跳 ready、EventBus ready，再启动 Host Agent 与其余所选 control 服务。必需依赖不能保持暂停；其他组件保留用户暂停。
5. 核验两个 current 和全部所选组件，持久写入完成日志后才移除主机级标记。相同请求重复执行且服务正常时不重启进程；已完成发布重启后缺少运行进程时，以 `repairing` 阶段恢复相同发布，不重新离线初始化或回退已提交的数据。

出错时先停两个单元的候选/旧进程，恢复持有安装标记的未完成单元，再撤销本次候选激活，最后按依赖启动日志记录的旧运行集合。首次安装的撤销只移除 current，保留退役目录及其数据。真实 SIGKILL 后日志与标记仍在，自动启动保持阻断；匹配原请求的编排器先恢复两个旧单元，再使用新尝试目录重新执行。不同请求不能接管未完成流程。完整软件、私密配置、数据库和失败诊断目录按私密权限保留，后续清理由部署保留策略负责。

普通本机验证：`make test-unit-bootstrap`。Linux 完整门禁 `make test-unit-bootstrap-linux` 只执行预先构建的制品，要求 `MOOX_BOOTSTRAP_TEST_BINARY`、`MOOX_RUNTIME_BINARY`，以及 `MOOX_BOOTSTRAP_HOST_ARCHIVE`/`MOOX_BOOTSTRAP_HOST_SHA256`、`MOOX_BOOTSTRAP_CONTROL_ARCHIVE`/`MOOX_BOOTSTRAP_CONTROL_SHA256`。六组必需场景不接受跳过：私密输入、输出边界、真实服务初始化/升级失败恢复、Admin 启动窗口实际 SIGKILL 的整机恢复、离线子进程的锁继承与父进程死亡，以及代理 CA 首次授权消费/历史导入。

实际服务场景使用完整 control 软件包中的 Admin、EventBus、Web Host、Console Proxy，以及完整 host 单元；没有声称验证全部九个 control 组件或三主机系统。顶层 `moox-cli setup bootstrap` 的配置生产和 SSH 传输、其余主机部署、生产历史状态归档、其他阶段的进程强杀、最终审查与正式验收仍待完成。

代理首次 internal CA 必须由请求的 `proxy_ca.create` 显式授权，或以 `proxy_ca.import_directory` 提供关闭后的规范源树；源树只需 `console-proxy/data` 和 `console-proxy/certs`，必须包含原始发布的根证书和指纹。创建和导入互斥，常规服务配置不接受初始化权限。`identity/console-proxy/receipt.json` 先记录 pending，完整材料在同级 `material/` 同步并原子发布后记录 ready；该目录独立于发布快照和候选清理。未发布的生成阶段可恢复，消费后材料丢失、损坏或主机不匹配则拒绝自动重新生成。升级复制最新关闭发布的状态，同时校验根身份与持久凭据一致；public-only 不生成多余 internal CA。

EventBus 角色和 TLS 身份保存在 Admin 的加密 secret 表，重跑复用；CA/服务端证书丢失一半或端点不匹配时拒绝替换，损坏证书在导出前拒绝。两个 EventBus 子进程与离线 bootstrap 使用相同的锁继承和父进程死亡约束。真实部署场景要求无角色客户端被拒绝，并核对升级前后的角色令牌和 EventBus CA 保持一致。
