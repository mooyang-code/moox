# moox-cli

运维命令行：初始化、部署、SCF 发布、元数据与历史数据导入、数据读取和诊断。

设计文档：[命令行工具](../../docs/模块/命令行工具.md)

## 构建与测试

```bash
./scripts/build/build.sh cli
./bin/moox-cli --help
go test -count=1 ./modules/cli/...
```

## 配置

读取仓库根目录的 `moox.toml`；默认业务配置在 `config/setup/`。
