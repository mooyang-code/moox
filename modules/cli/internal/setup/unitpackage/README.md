# 部署软件包

`unitpackage` 统一六类软件包的构建、检查和解包，供操作员 CLI 与远端 `moox-runtime` 共用。它不依赖旧部署器，不读取 `moox.toml`、SSH 凭据、运行密钥或数据。Linux amd64/arm64 的软件制品须预先构建；该包不会隐式编译或启动服务。

- `Package(ctx, Options)` 只收集已编译的 Linux ELF 和 Git 跟踪的公开模板，输出可重复的 `.tar.gz` 与摘要。
- `Inspect(ctx, archive)` 校验归档，不写文件。
- `Extract(ctx, ExtractOptions)` 在私密暂存目录内复用同一流式校验器，全部成功后原子发布到新的目标目录。

软件包类型为 `host`、`control`、`storage`、`access`、`egress-proxy`、`trade`。`Components(profile)` 给出服务边界，`Binaries(profile)` 同时包含相应 CLI/助手。主机网关、Host Agent 和共用运行助手只进入 host 包；业务包不携带主机服务。归档只包含二进制、共享服务目录、配置/资源模板和 schema，不含凭据、持久数据或生命周期脚本。安装器负责生成后者。

`package-manifest.json` 使用唯一的规范 JSON 编码，包含版本、平台、目录摘要、组件及逐文件路径/权限/长度/SHA256。检查器拒绝越界路径、链接、重复条目、无效 ELF、错误平台或组件、清单缺项、额外文件、多余 tar/gzip 载荷及流中断。归档大小与展开文件内容总量各不超过 1 GiB，单文件不超过 512 MiB，载荷最多 4096 个文件。

解包必须提供软件生产端的 `sha256:<64位小写hex>`、预期 profile、GOOS/GOARCH 和新目标目录。目标路径必须绝对、规范；父目录必须实际存在、由当前用户持有且不可由组或其他用户写入，路径不能经过父目录链接。私密暂存目录及其子目录为 0700，文件以 0600 创建，写入并校验后设置清单权限、fsync。所有归档内容、外部摘要和元数据核对完成后才发布。

解包通过 `os.Root` 限定写入范围，并以排他创建拒绝路径冲突。Linux 使用 `renameat2(RENAME_NOREPLACE)`、macOS 使用 `renameatx_np(RENAME_EXCL)` 发布；已有文件、目录或链接均不覆盖，同一目标的并发解包只有一个成功。失败或取消会清理本次未发布目录；进程被强杀留下的 `.extract-*` 不作为可用发布。发布后父目录 fsync 失败时，API 返回非空结果及错误，调用方须检查该目录后处理重试。

操作员入口保持为：

```bash
moox-cli setup package --profile host --binary-dir /absolute/path/binaries --output /absolute/path/host.tar.gz
moox-cli setup package inspect /absolute/path/host.tar.gz
```

远端助手只为自身所在 Linux 平台解包：

```bash
moox-runtime extract --archive /absolute/path/host.tar.gz \
  --sha256 'sha256:<软件生产端摘要>' --profile host \
  --destination /absolute/physical/releases/new-release
```

`extract` 只准备软件，不写 `current`、暂停/PID 状态、身份、数据或私密运行计划。准备阶段不占用维护锁；安装器在配置注入、停止旧实例、复制持久状态和切换目录时必须持有共用维护锁。业务配置渲染、身份注入、数据与 CA 保留、激活、回滚、bootstrap 和实际部署仍须接入，软件解包通过不等于完整安装验收。

本机基础测试为 `make test-deployment-packages`。Linux 完整门禁执行本机预构建的测试制品、助手和实际 host 软件包，不在执行主机编译：

```bash
MOOX_UNIT_PACKAGE_TEST_BINARY=/absolute/path/unitpackage.test \
MOOX_RUNTIME_BINARY=/absolute/path/moox-runtime \
MOOX_HOST_SOFTWARE_PACKAGE=/absolute/path/host.tar.gz \
MOOX_HOST_PACKAGE_SHA256='sha256:<软件生产端摘要>' \
make test-unit-package-linux
```

门禁要求十组软件包/解包场景全部执行且无跳过，包括实际 host 包解包及包内助手执行、错误架构拒绝、六类双架构边界、篡改拒绝、现有对象保留与并发发布。普通 Linux 模块测试在缺少实际助手或 host 包时跳过两项集成场景，不能作为完整门禁证据。
