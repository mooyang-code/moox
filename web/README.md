# MooX Web

MooX 管理台前端，基于 Vue 3、Vite 5、TypeScript、Pinia 与 Arco Design。它负责登录、Space 上下文、数据资产、采集与云函数、交易、运维等管理页面。

## 运行环境

- Node.js >= 18.12.0
- pnpm >= 8.7.0

## 常用命令

```bash
pnpm install
pnpm dev
pnpm build:dev
pnpm build:prod
pnpm preview
```

仓库根目录的 `./scripts/build.sh web-host` 只构建当前已嵌入 statik 资源的 `moox-web-host` 二进制。需要重建前端资源时，先运行 `pnpm build:prod`，再到 `web-host` 目录执行 `make statik`；也可以直接使用 `./scripts/deploy-moox.sh --build-web-assets`。

## 本地联调

前端不通过 Vite 代理或 `web-host` 转发 API。运行时从浏览器当前 URL 读取 hostname，并使用 `VITE_GATEWAY_PORT` 指定的网关端口，默认 `11000`。

```bash
# 1. 启动管理网关
cd ../modules/admin
go run ./cmd/moox-admin -conf=config/trpc_go.yaml

# 2. 按需启动后端模块
cd ../storage
MOOX_STORAGE_CONFIG=config/storage.yaml go run ./cmd/moox-storage -init-metadata -conf=config/trpc_go.yaml
MOOX_STORAGE_CONFIG=config/storage.yaml go run ./cmd/moox-storage -conf=config/trpc_go.yaml

cd ../cloudnode
go run ./cmd/moox-cloudnode -conf=config/trpc_go.yaml

cd ../collector
go run ./cmd/moox-collector -conf=config/trpc_go.yaml

# 3. 启动前端
cd ../../web
pnpm dev
```

请求路径：

- 管理台请求：`http(s)://{当前hostname}:11000/api/admin/{service}/{method}`
- Storage 管理请求：`/api/admin/storage_metadata|storage_access|storage_view/{method}`
- Trade 管理请求：`/api/admin/trade_*/*`
- SCF / collector 等后台服务请求：`/api/service/{service}/{method}`，前端不直接调用

`web-host` 只提供静态资源，收到 `/api/*` 会返回 404，用来暴露错误的代理依赖。

## 目录结构

```text
build/            Vite 构建配置
public/           静态资源
src/api/          后端 API 封装
src/assets/       前端静态资源
src/components/   通用组件
src/config/       全局配置
src/hooks/        组合式函数
src/layout/       管理台布局
src/router/       路由
src/store/        Pinia 状态
src/style/        全局样式
src/utils/        工具函数
src/views/        页面
```

## 主要页面

- 登录与用户认证
- 系统设置：空间、密钥、服务部署信息
- 数据资产：数据源、数据对象、数据集、字段、因子、查询视图
- 数据同步与数据列表
- 采集任务、任务实例、云函数、代码包版本
- 交易账户、订单、持仓、流水
- SSH 终端与主机监控

## 配置

`web/.env.development`：

```dotenv
VITE_GLOB_APP_TITLE=MooX
VITE_GATEWAY_PORT=11000
```

`web/.env.production` 保持同样的运行时网关模型，通过当前访问域名和 `VITE_GATEWAY_PORT` 拼出后端地址；未配置 `VITE_GATEWAY_PORT` 时默认使用 `11000`。
