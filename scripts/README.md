# 脚本目录约定

`scripts/` 只保留按职责归档的正式脚本，不提供旧路径兼容入口。新增脚本应放入下列目录之一：

| 目录 | 职责 |
| --- | --- |
| `build/` | 编译二进制、构建运行时包和服务包 |
| `check/` | 静态检查、架构检查和事件契约检查 |
| `ci/` | CI 环境辅助逻辑 |
| `deploy/` | 因子引擎、归档 Worker 的安装与本地证书 |
| `marketdata/` | 行情日历维护和校验 |
| `test/contract/` | 源码、配置和打包契约测试 |
| `test/e2e/` | 需要真实进程或外部依赖的端到端测试 |
| `test/gates/` | 执行预构建制品的 Linux 部署与运行时门禁 |

发布产物由 `release/build-release.sh` 生成（每个组件一个包，由 `moox-cli setup package-components` 按服务目录打）；部署、启停与日志都由 `moox-cli setup` 完成，目标机上的安装脚本内嵌在 CLI 里（`modules/cli/internal/setup/deploy/install.sh`）。

脚本应通过 `${BASH_SOURCE[0]}` 计算仓库根目录，不依赖当前工作目录。跨目录调用必须使用完整的规范路径。
