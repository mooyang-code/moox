# MooX Web

MooX 管理台前端，基于 Vue 3、Vite 6、TypeScript、Pinia 与 Arco Design。
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

页面只调用同源的 `/api/admin/{service}/{method}`，不直接访问业务服务；会话在 HttpOnly Cookie 里，只发给同源请求。
本地 `pnpm dev` 时在 `.env.development` 中设置 `VITE_ADMIN_PROXY_TARGET` 指向一个可用的管理入口
（例如 `https://127.0.0.1:9527`），开发服务器把 `/api/admin`（含 WebSocket）与 `/ca` 代理过去；留空则不代理。生产构建不用这个代理。
控制机 `console.yaml` 的 `console.allowed_origins` 要加上开发服务器的来源（例如 `http://localhost:9527`）；内部 CA 不在 Node 的信任里时，用 `NODE_EXTRA_CA_CERTS` 指向根证书。

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

在仓库根目录执行 `scripts/build/build.sh admin`：它先在 Docker 里按当前源码构建前端，再把 `dist/` 内嵌进 `moox-admin`
（编译时临时拷到 `modules/admin/internal/webui/dist`，编译完删掉）。
