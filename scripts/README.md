# 脚本目录约定

根目录保留构建、发布和部署的稳定入口，例如 `build.sh`、`release.sh`、
`release-matrix.sh` 和 `deploy-moox.sh`。这些路径会被 Makefile、发布包和契约测试引用，不能随意移动。

脚本按职责归档：

- `tests/contract/`：静态检查、部署契约、发布契约和边界测试。
- `tests/e2e/`：需要启动服务或访问外部依赖的端到端测试。
- `checks/`：被 Makefile、契约测试和 CI 复用的源码边界检查器。
- `e2e/`：由其他测试组合调用的场景入口。
- `lib/`：可复用的 Shell 函数和部署辅助逻辑。
- `ci/`、`deps/`：CI 辅助及第三方依赖校验文件。

为保持已有命令可用，根目录中的 `test-*.sh` 是指向 `tests/contract/` 或
`tests/e2e/` 的兼容入口。新脚本应直接放入对应子目录；文档和 Makefile 可以继续使用根入口。

运行时辅助脚本（例如 `storage-start.sh`、`storage-stop.sh`、
`reset-storage-view-indexes.sh`）不放入测试目录，因为它们会被发布脚本复制到目标机器。
