# 构建

构建一律在本机进行，环境准备、各系统的前置依赖、构建与发布步骤见 [`docs/构建手册.md`](../../../docs/构建手册.md)。

最常用的几条（在仓库根目录运行）：

```bash
# 检查本机缺什么，并给出本机系统上的安装命令；Agent 用 --json
bash scripts/build/check-build-env.sh --profile all

# 构建 linux/amd64 二进制：纯 Go 的在本机交叉编译，含 CGO 的（Storage）自动进 Docker；产物默认在 bin/
bash scripts/build/build-linux-local.sh admin collector storage-primary

# 本机自己用的二进制（例如 moox-cli）；加 TARGET_GOOS / TARGET_GOARCH 可编成别的系统
./scripts/build/build.sh cli
```

- Go 版本以仓库根的 `.go-version` 为准，构建脚本要求完全一致；没有就运行 `bash scripts/build/install-go.sh`。
- 构建目标即 `scripts/build/build.sh` 的参数，`build-linux-local.sh` 不支持 `all`，要逐个写明目标。
- 边界检查：`make check-boundaries`；推送前的本地预检：`bash scripts/ci/local-verify.sh`。
- 发布到线上见 [`native-release.md`](native-release.md)。
