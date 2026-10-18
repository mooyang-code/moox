# 部署运行助手

`moox-runtime` 随 host 软件包安装，供同机所有业务包共用。进程操作读取部署器生成的 `runtime.json` 和私密环境文件，不读取运维 `moox.toml`，不连接 SSH，不是服务目录中的常驻组件。仅在 Linux 执行进程操作，amd64/arm64 都是纯 Go 制品，可在 macOS 关闭 CGO 编译。

每个实际发布目录包含 0600 的 `runtime.json`：

```json
{
  "version": 1,
  "host_id": "control",
  "deployment_root": "/data/moox",
  "release_root": "/data/moox/prod/releases/release-id",
  "catalog_sha256": "sha256:<当前内嵌服务目录摘要>",
  "components": [
    {
      "id": "console-proxy",
      "environment_file": "/data/moox/prod/releases/release-id/secrets/runtime-console-proxy.json"
    }
  ]
}
```

根路径必须是现有、由当前用户持有、无组或其他用户写权限的物理目录。命令可通过 `current/runtime.json` 的目录视图读取，但计划文件自身必须是普通文件。组件 ID、二进制名和健康端口来自同一内嵌服务目录；重复/未知组件、错误目录摘要、越界环境路径及非普通文件拒绝执行。组件工作目录为 `<release_root>/<组件ID>`，二进制为 `<release_root>/bin/<目录中的二进制名>`。环境文件为 0600 JSON 字符串映射，只接受 `MOOX_*`；必须包含三项健康 HMAC 配置。助手重新计算制品摘要并注入进程身份，过滤调用进程遗留的 `MOOX_*` 环境变量。签名与探针密钥不进入参数、PID 清单或命令结果。

支持 `start`、`stop`、`restart`、`pause`、`resume`、`healthcheck`、`status`，用 `--plan PATH` 指定计划，`--components ID,...` 可选择该计划中的组件。计划顺序为启动顺序，整组停止与暂停逆序执行；整组重启先停止全部目标，再启动。运行时状态统一放在 `<deployment_root>/run`，不随业务或主机发布目录切换：

- `host.json` 固定该部署根的规范主机 ID；不同主机身份不能复用该根。
- `maintenance.lock` 是操作系统文件锁。安装器、启停、暂停、恢复和守护检查共用；守护发现锁被占用时整体跳过。
- `paused/<组件>` 是持久暂停标记。暂停先写完所有目标标记，再停止进程；启动、重启和守护都保留并识别它。恢复先验证制品与环境，再清除标记并启动；失败时恢复标记。
- `pids/<组件>.json` 保存进程启动时的 executable/device/inode、UID、Linux start ticks、制品摘要、随机 boot ID 及启停预算。Linux pidfd 将信号绑定到原进程，身份不符拒绝发送信号。
- `draining/<组件>` 保存已发送 TERM 的时间和完整预算。停止取消或中断后保留，守护不得重启；再次显式停止继续原预算，完成后清理。

console-proxy 必须在它的 0600 `config/app.yaml` 中明确声明 `drain_timeout`、`engine_stop_timeout`、`cleanup_margin` 和 `startup_timeout`，每项为正且不超过 10 分钟。外层停止等待前三项之和；使用运行中的旧发布记录，新的更短配置不能缩短它。其余组件使用两分钟停止预算。超过预算后再次核对原进程身份，才通过 pidfd 发送 KILL，并观察进程确实退出后释放锁、清理记录或启动替代进程。

守护只用带 HMAC 的 loopback `/healthz` 决定是否重启；`/readyz` 暂不可用不触发重启。健康认证失败、重定向或返回的制品身份不符会报告错误，不自动结束进程。HTTP 请求不使用环境代理或跟随重定向。

启动采用父子管道屏障：子进程先等待，父进程持久化最终可执行文件、启动器身份和预算后才允许 exec。同一 PID/start ticks 在启动器和最终进程之间连续保留；父进程在记录之前退出，管道 EOF 使子进程退出，避免未登记的后台服务。记录之后中断可由新助手继续等待就绪或停止，成功就绪后删除临时启动器身份。健康响应同时核对制品摘要和本次随机 boot ID，避免相同制品的其他实例被误认；重复 start 会探测运行中旧发布的 readiness。

调用方已经持有维护锁时，传递同一文件描述符到助手（通常为 FD 3），同时使用 `--maintenance-lock-held --maintenance-lock-fd 3`。助手核对文件身份和已有的独占 flock，再复用同一个锁；未加锁、共享锁或只有布尔参数均失败，不通过加锁/升级来补足无效继承。已有锁由 Linux [`/proc/self/fdinfo`](https://www.kernel.org/doc/html/latest/filesystems/proc.html) 的 `FLOCK ADVISORY WRITE` 记录核对。

Go 编排器通过 `Maintenance.UseLock` 同步调用准备、封存、激活等持锁库。传出的选项绑定主机/部署根并在回调结束时失效；已进入的操作结束后才释放描述符，防止编号复用使过期选项再次通过。回调使用所提供的选项，不在其中调用同一个 guard 的方法。

五步 bootstrap 另使用 `BeginBootstrap` / `EndBootstrap`，将请求摘要与随机令牌写入部署根的 0600 `run/bootstrap.json`。此标记跨 host/control 单元与进程退出保留，和每个单元的安装标记、用户暂停标记独立。没有已核验继承锁的 start/restart/resume 拒绝，healthcheck 跳过；其他安装入口也不能取得普通维护回调。只有携带匹配 `Options.BootstrapID` 的编排器可以重新取得维护锁并接管同一请求，单元完成不能清除整个 bootstrap 标记，仍有未完成单元安装时不能结束 bootstrap。完整编排器、恢复日志和 CLI 入口仍由 G3 接通；这里的实际 SIGKILL 验证只证明运行层保护窗口。

Linux 读取进程的可执行文件时，先打开 `/proc/PID/exe` 固定同一执行镜像，再从该描述符读取路径和 inode，避免 exec 切换瞬间拼出不一致的身份。内核回归持续在同一 PID 上切换两个真实 ELF，要求每次快照中的路径与 inode 一致。

软件准备另提供 `extract --archive PATH --sha256 DIGEST --profile PROFILE --destination NEW_DIR`，只为助手所在 Linux 平台解包已核验软件。它复用独立的[软件包模块](../unitpackage/README.md)，原子发布到新目录且不覆盖已有对象；不写 current、运行计划、身份或持久数据，也不启动服务。准备阶段可以在当前版本运行时执行，后续激活仍须使用共用维护锁。

身份检查提供 `inspect-bundle`，复用独立的[身份消费模块](../unitbundle/README.md)，按调用方提供的拓扑、CA 和快照核对私密目录，仅输出公开清单。

```bash
make test-unit-runtime
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build/build.sh runtime
```

Linux 完整门禁执行事先构建的测试制品和真实运行助手/console-proxy，不在执行主机编译：

```bash
MOOX_UNIT_RUNTIME_TEST_BINARY=/absolute/path/unitruntime.test \
MOOX_RUNTIME_BINARY=/absolute/path/moox-runtime \
MOOX_RUNTIME_PROXY_BINARY=/absolute/path/moox-console-proxy \
make test-unit-runtime-linux
```

门禁要求十四组 Linux 内核/真实代理场景全部执行，不允许跳过。普通模块测试缺少真实代理制品时会跳过那一项，不能作为完整门禁证据。测试需要独占合成服务的健康端口；已有业务占用时，可在支持的 Linux 上用 `bwrap --unshare-net --bind / / --dev /dev --proc /proc -- bash scripts/test/gates/test-unit-runtime-linux.sh` 隔离运行，不停止现有业务。[发布准备层](../unitinstall/README.md) 已生成七个生命周期包装脚本，并在同一维护锁下写入身份、私密运行计划和环境；`prepare --request PATH` 准备新发布，`inspect-release --directory PATH` 核验尚未启动的候选目录。发布激活/回滚已共用本运行层，并以持久安装标记保护安装器退出后的恢复。实际 CLI 部署、bootstrap 和 `setup pause/resume` 仍须由 G2/G3/G6/G11 接线；这些进展不代表完整安装或正式发布已经完成。
