# MooX Web

MooX 管理台前端，基于 Vue 3、Vite 5、TypeScript、Pinia 与 Arco Design。
设计说明见[前端](../docs/模块/前端.md)。

## 运行环境

- Node.js `^20.19.0 || ^22.13.0 || >=24.0.0`（与 `package.json` 的 engines 一致）；推荐按 `.nvmrc` 使用 Node 24
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

生产构建只调用同源的 `/api/admin/{service}/{method}`，不使用 Vite 代理，也不直接访问业务服务。
本地 `pnpm dev` 时在 `.env.development` 中设置 `VITE_ADMIN_ORIGIN` 指向一个可用的管理入口
（例如 `https://127.0.0.1:9527`），开发模式的 HTTP 与 WebSocket 请求都会发往该地址；留空则使用当前页面的 origin。生产构建忽略这个覆盖项。跨域联调时，Admin 的 CORS 配置须允许开发页面的 origin，浏览器也须信任入口证书。

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

`pnpm build:prod` 生成 `dist/` 后，在 `../web-host` 执行 `make statik` 把资源嵌入 `moox-web-host`；
原生部署（`setup bootstrap` / `setup release build`）会自动完成这两步。
