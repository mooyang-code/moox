# moox-admin

控制面：控制台 API（管理台的唯一入口）、登录认证、业务空间、主机与部署、网关控制（为各主机网关下发路由快照）、
调用方密钥与 PKI、密钥、SSH、系统初始化和采集发布租约。

设计文档：[管理后台](../../docs/模块/管理后台.md)

## 构建与测试

```bash
./scripts/build/build.sh admin
./scripts/build/build.sh admin-cli
go test -count=1 ./modules/admin/...
```

## 配置

`config/trpc_go.yaml`（服务与端口）、`config/app.yaml`、`config/console.yaml`（控制台 API）；schema 在 `schema/admin.sql`。
