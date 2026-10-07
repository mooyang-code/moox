# moox-admin

控制面：登录认证、业务空间、服务目录、密钥、SSH、系统初始化和采集发布租约；管理台 API 的唯一入口，并为各节点网关下发路由。

设计文档：[管理后台](../../docs/模块/管理后台.md)

## 构建与测试

```bash
./scripts/build/build.sh admin
./scripts/build/build.sh admin-cli
go test -count=1 ./modules/admin/...
```

## 配置

`config/trpc_go.yaml`（服务与端口）、`config/app.yaml`、`config/gateway.yaml`（管理网关）；schema 在 `schema/admin.sql`。
