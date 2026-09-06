# Strategy 前端工作台验收记录

本文记录现代 Strategy 前端工作台的验证边界和本次执行证据。页面只使用
`Strategy`、`StrategyInstance`、`StrategyResult` 和 `ListStrategyTargets`，不把旧
Runner、成交回报或本地推导状态当成策略事实。

## 验收边界

- 策略定义页只保存 `strategy_id + dsl_yaml`，编辑器的 YAML 检查不等同于运行校验。
- 实例创建只从当前 Space 的 View、Factor、Binding、结果列和逻辑账户元数据生成绑定，提交时固定 `enabled=false`。
- 实例详情分别展示当前目标和历史结果；目标权重不等于成交数量，`sent` 不等于成交。
- 停用不执行清仓；停用后残留 `session_id` 只表示后台控制尚未完成，不能推导为清仓成功。
- 当前目标按 `enabled/session_id/bar_end_time/valid_until` 判定为无结果、零仓位、有效、过期或未知；接口失败保留独立错误态。
- 远端验收只做页面和读取链路，不创建、编辑策略或启停真实交易实例。

## 本地验证

在 `web` 目录执行：

```bash
pnpm vitest run src/api/strategy.test.ts src/store/modules/strategy.test.ts src/views/strategy
pnpm test
pnpm exec vue-tsc --noEmit
pnpm exec playwright test tests/strategy-console.spec.ts --config=playwright.strategy.config.ts
pnpm build:prod
make -C ../web-host statik
make -C ../web-host build-linux
pnpm check:menu
git -C .. diff --check
```

本次执行结果：

- Vitest：64 个测试文件、223 个测试通过；策略聚焦测试 9 个文件、26 个测试通过。
- Playwright：策略工作台 3/3 通过，使用独立本地 19527 mock 配置。
- `vue-tsc`、生产构建、静态资源嵌入和 `web-host` Linux 构建通过。
- `check:menu` 通过。
- `check:detail-pages` 未通过：仓库既有脚本引用不存在的
  `web/src/views/ops/storage/routes.vue`，与本次策略前端改动无关；未修改该无关脚本。

## 代码与制品

- 前端提交：`e45679ee`（包含 `6aaefffb` 的工作台重构）。
- 分支：`feature/mooyang`，已推送到 `origin/feature/mooyang`。
- 新起 codeCR Agent 已复审现代 API、DSL 绑定、空间/会话隔离、控制未知态和请求竞态；
  复审未发现 P0/P1。其提出的历史范围、目标/结果独立提交和控制状态问题已修复并重新验证。

## 正式环境验证

- 目标主机来自 `moox.toml` 的 `[strategy_host]`，仅替换并重启 `web-host`，没有重启
  Strategy、Trade 或其他服务。
- 远端 `/data/moox/prod/bin/moox-web-host` SHA256：
  `a85cab79926bdd9d041adbe27afa8139b66143b3f368a3e9d09485fa9f15261a`，与本地构建制品一致。
- HTTPS 登录页、登录会话、策略菜单、运行实例路由和错误态均已在
  `https://106.53.107.122:9527/` 验证。
- 远端 `readyz` 返回 HTTP 401，说明接口受健康认证保护且进程可响应。
- 运行实例读取 `ListStrategies` 时，远端 Strategy 服务当前返回
  `http client transport ... 127.0.0.1:11430 ... EOF`。因此正式环境能证明静态前端已发布，
  但不能证明当前远端策略数据链路可读；该运行时故障属于 Strategy 服务，需要单独排查，
  本次前端交付不修改后台业务或配置。
