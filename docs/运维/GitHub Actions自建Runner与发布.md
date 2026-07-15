# GitHub Actions 自建 Runner 与发布

MooX 使用 GitHub Actions 编排 CI/CD，并将可信的 `main`、Tag 和手工发布任务路由到
`106.53.107.122` 上的 `moox-ci` self-hosted runner。

## 工作流边界

- `pull_request` 使用 GitHub-hosted runner，避免公开仓库的 fork PR 在个人服务器上执行。
- `main` push 和手工 CI 使用 `moox-ci` runner。
- `v*` Tag 触发 `Release and deploy MooX`，完成校验、Linux amd64 打包、GitHub Release、生产部署和健康检查。
- 生产部署使用 GitHub Environment `production`，可在仓库设置中增加 required reviewer。

## 服务器安装

以下命令以 `ubuntu` 用户执行，Runner 安装目录为 `/home/ubuntu/actions-runner`：

```bash
sudo apt-get update
sudo apt-get install -y curl git jq gh make rsync openssl gcc ripgrep python3-venv
mkdir -p /home/ubuntu/actions-runner
cd /home/ubuntu/actions-runner
```

服务器需要预装 Go 1.24、Node 22、pnpm 10.28.2，以及因子测试需要的 Python 运行时：

```bash
python3 -m venv /home/ubuntu/.venvs/moox
/home/ubuntu/.venvs/moox/bin/pip install \\
  -r /home/ubuntu/moox/src/modules/factor/pyworker/runtime-requirements.txt
```

当前机器已有 Go SDK；Node 可以安装到 `/usr/local/node`，并将 Go SDK 和 Python venv 的 `bin` 目录加入 systemd Runner 的 `PATH`。

### 获取 Runner 注册 Token

注册 Token 不是长期凭据，需要在拥有仓库管理 Runner 权限的机器上临时生成。可以通过
GitHub 页面或 `gh` CLI 获取：

- 页面：`Settings -> Actions -> Runners -> New self-hosted runner`
- CLI：先确认 `gh` 已登录到目标 GitHub 账号，再执行：

```bash
gh auth status
gh api -X POST repos/mooyang-code/moox/actions/runners/registration-token --jq .token
```

命令输出的值就是 `<REGISTRATION_TOKEN>`，复制给服务器上的 `config.sh` 使用。Token
短时有效且仅用于注册，过期或注册完成后不要保存、提交到仓库或写入日志；重新注册时需要重新生成。

官方说明：[添加 Self-hosted Runner](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners)。

从 GitHub Actions Runner releases 页面取得当前 x64 Linux Runner 版本，然后在服务器执行：

```bash
RUNNER_VERSION=2.335.1
curl -fL -o actions-runner.tar.gz \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
tar -xzf actions-runner.tar.gz
./config.sh \
  --url https://github.com/mooyang-code/moox \
  --token <REGISTRATION_TOKEN> \
  --name moox-106-53-107-122 \
  --labels moox-ci,linux,x64 \
  --work _work \
  --unattended
sudo ./svc.sh install ubuntu
sudo ./svc.sh start
```

Runner 需要能够对 GitHub 建立出站 HTTPS 连接。Workflow 使用服务器预装的 Go、Node、pnpm 和 Python；服务器上的 `gh` 用于创建 GitHub Release。

检查状态：

```bash
sudo ./svc.sh status
```

然后在 GitHub 仓库 `Settings -> Actions -> Runners` 中确认 Runner 为 `Idle`，并包含 `moox-ci`、`linux`、`x64` 标签。

## 发布

发布前先把变更合并到 `main`，再创建并推送 Tag：

```bash
git tag -a v0.1.0 -m 'release v0.1.0'
git push origin v0.1.0
```

发布流水线会调用现有的 `make verify`、`make release` 和 `scripts/deploy-moox.sh`。部署目标固定为本机的 `/home/ubuntu/moox/prod`，不会执行 `--reset-data`，并会使用现有运行数据和密钥。

## 运行机器维护

```bash
df -h /home/ubuntu
sudo ./svc.sh status
sudo journalctl -u actions.runner.mooyang-code-moox.moox-106-53-107-122.service -n 100 --no-pager
```

不要把生产密钥放入仓库、Actions cache 或构建产物。Runner 更新、系统安全更新和磁盘空间需要定期检查。
