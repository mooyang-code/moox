# MooX Web

MooX 管理台前端，基于 Vue 3、Vite 5、TypeScript、Pinia 与 Arco Design。
设计说明见[前端](../docs/模块/前端.md)。

## 运行环境

- Node.js >= 18.12（jsdom 测试需要 Node 20.19+ 或 22.12+）
- pnpm >= 8.7

## 常用命令

```bash
pnpm install
pnpm dev                      # 本地开发服务器
pnpm test                     # vitest 单元测试
pnpm build:prod               # vue-tsc 类型检查 + 生产构建，输出 dist/
pnpm lint:eslint:check        # ESLint（零 warning）
pnpm lint:prettier:check      # Prettier
pnpm check:menu               # 其余 check:* 为页面结构契约检查脚本
```

## 本地联调

前端只调用同源的 `/api/admin/{service}/{method}`，不使用 Vite 代理，也不直接访问业务服务。
本地 `pnpm dev` 时在 `.env.development` 中设置 `VITE_ADMIN_ORIGIN` 指向一个可用的管理入口
（例如 `https://127.0.0.1:9527`），请求就会发往该地址；留空则使用当前页面的 origin。

- 管理接口：`/api/admin/{service}/{method}`
- Storage：`/api/admin/storage/{method}`（Admin 的 Storage BFF）
- SSH 终端：WebSocket `/api/admin/ssh/WsConnect`

## 目录结构

```text
build/            Vite 插件配置
public/           静态资源
scripts/          页面契约检查脚本（node）
tests/            跨页面的契约与 e2e 测试
src/api/          后端 API 封装
src/components/   通用组件（自动注册）
src/layout/       管理台布局
src/router/       路由；菜单在 src/api/modules/system/static-menu.ts
src/store/        Pinia 状态
src/views/        页面
```

## 发布

`pnpm build:prod` 生成 `dist/` 后，在 `../web-host` 执行 `make statik` 把资源嵌入 `moox-web-host`，
再用 `moox-cli setup deploy-service --component web-host` 部署。
