import { expect, test, type Page } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

async function installShell(page: Page) {
  await page.route("**/api/admin/auth/GetUserInfo", route => route.fulfill({ json: { ret_info: { code: 0, msg: "ok" }, user_info: { user_id: "u1", username: "admin", nickname: "Admin", role: 2 } } }));
  await page.route("**/api/admin/space/ListSpaces", route => route.fulfill({ json: { ret_info: { code: 0 }, spaces: [{ space_id: "space-1", name: "测试空间", status: "active" }], page_result: { page: 1, size: 20, total: 1, has_more: false } } }));
  await installE2ESession(page, "space-1");
}

test("strategy definitions render the real DSL contract", async ({ page }) => {
  await installShell(page);
  await page.route("**/api/admin/strategy/ListStrategies", route => route.fulfill({ json: { ret_info: { code: 0 }, strategies: [{ strategy_id: "momentum", strategy_name: "动量策略", dsl_yaml: "name: 动量策略\ndata: {bar: 1h, calendar: crypto_24x7}\ntriggers: {event: {name: source.ready}}\nrules: {rank: {pool: [BTC], score: close, select: {top: 1}, weight: 0.6}}}", created_at: "2026-07-30T00:00:00Z" }], total: 1, page: 1, page_size: 20 } }));
  await page.goto("/#/strategy/overview");
  await expect(page.getByRole("heading", { name: "策略定义", exact: true })).toBeVisible();
  await expect(page.getByText("动量策略")).toBeVisible();
  await expect(page.getByText("DSL 是唯一策略配置来源")).toBeVisible();
  await expect(page.getByText("Runner")).toHaveCount(0);
});

test("instance detail distinguishes valid target weight from trade fills", async ({ page }) => {
  await installShell(page);
  await page.route("**/api/admin/strategy/GetStrategyInstance", route => route.fulfill({ json: { ret_info: { code: 0 }, instance: { instance_id: "instance-paper", strategy_id: "momentum", space_id: "space-1", input_bindings_json: "{\"source_view_id\":\"ohlcv\"}", logical_account_id: "logical-paper", enabled: true, session_id: "session-1", updated_at: "2026-07-30T00:00:00Z" } } }));
  await page.route("**/api/admin/strategy/GetStrategy", route => route.fulfill({ json: { ret_info: { code: 0 }, strategy: { strategy_id: "momentum", strategy_name: "动量策略", dsl_yaml: "name: 动量策略" } } }));
  await page.route("**/api/admin/strategy/ListStrategyTargets", route => route.fulfill({ json: { ret_info: { code: 0 }, targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.25" }], session_id: "session-1", bar_end_time: "2026-07-30T00:00:00Z", valid_until: "2099-07-30T00:00:00Z" } }));
  await page.route("**/api/admin/strategy/ListStrategyResults", route => route.fulfill({ json: { ret_info: { code: 0 }, results: [{ result_id: "result-7", instance_id: "instance-paper", session_id: "session-1", period_time: "2026-07-30T00:00:00Z", valid_until: "2099-07-30T00:00:00Z", targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.25" }], publish_status: "sent", rule_states_json: "{}" }], total: 1 } }));
  await page.goto("/#/strategy/detail/instance-paper");
  await expect(page.getByRole("heading", { name: "instance-paper" })).toBeVisible();
  await expect(page.getByText("25.00%")).toBeVisible();
  await page.getByText("历史结果", { exact: true }).click();
  await expect(page.getByText("已发送")).toBeVisible();
  await expect(page.getByText("投递状态不代表成交状态")).toBeVisible();
  await expect(page.getByText("停用实例")).toBeVisible();
});

test("组合账户暂停时展示待执行目标和逐账户清仓结果", async ({ page }) => {
  await installShell(page);
  const logicalAccount = { logical_account_id: "logical-paper", name: "Paper 组合", execution_mode: 1, market_type: 1, settlement_asset: "USDT", automation_state: "PAUSED", pause_reason: "manual review", owner_runner_id: "runner-paper", ready: true, readiness_reasons: [], members: [{ trading_account_id: "binance-paper", enabled: true, priority: 0 }] };
  await page.route("**/api/admin/trade_console/ListLogicalAccounts", route => route.fulfill({ json: { ret_info: { code: 0 }, logical_accounts: [logicalAccount], page_result: { page: 1, size: 20, total: 1 } } }));
  await page.route("**/api/admin/trade_console/GetLogicalAccount", route => route.fulfill({ json: { ret_info: { code: 0 }, logical_account: logicalAccount } }));
  await page.route("**/api/admin/trade_console/GetLogicalAccountTarget", route => route.fulfill({ json: { ret_info: { code: 0 }, target: { target_id: "result-8", logical_account_id: "logical-paper", runner_id: "runner-paper", command_sequence: "8", targets: [{ instrument_id: "ETH-USDT-SPOT", quantity: "2" }], status: "ACKED" } } }));
  await page.route("**/api/admin/trade_console/FlattenLogicalAccount", route => route.fulfill({ json: { ret_info: { code: 0 }, action: { action_id: "flatten-1", logical_account_id: "logical-paper", action_type: "FLATTEN", reason: "risk cleanup", status: "PARTIAL", result_json: JSON.stringify({ accounts: [{ trading_account_id: "binance-paper", status: "PARTIAL", remaining_positions: [{ instrument_id: "ETH-USDT-SPOT", quantity: "0.2", reason: "minimum quantity" }], error: "minimum quantity" }] }) } } }));
  await page.goto("/#/trading/accounts?view=strategy&logical_account_id=logical-paper");
  await expect(page.getByRole("tab", { name: "组合账户", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("当前完整目标序号 8 已保存但不会执行；恢复后会继续收敛。")).toBeVisible();
  await page.getByRole("button", { name: "逐账户清仓" }).click();
  const modal = page.locator(".arco-modal:visible").filter({ hasText: "逐账户清仓" });
  await modal.locator("input").nth(0).fill("flatten-1");
  await modal.locator("input").nth(1).fill("risk cleanup");
  await modal.getByRole("button", { name: "确定" }).click();
  await expect(page.getByText("0.2 (minimum quantity)")).toBeVisible();
  await expect(page.getByText("部分完成").first()).toBeVisible();
});
