import { expect, test, type Page } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

async function installShell(page: Page) {
  await page.route("**/api/admin/auth/GetUserInfo", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0, msg: "ok" },
        user_info: { user_id: "u1", username: "admin", nickname: "Admin", role: 2 }
      }
    })
  );
  await page.route("**/api/admin/space/ListSpaces", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0, msg: "ok" },
        spaces: [{ space_id: "space-1", name: "测试空间", status: "active" }],
        page_result: { page: 1, size: 20, total: 1, has_more: false }
      }
    })
  );
  await installE2ESession(page, "space-1");
}

test("strategy console lists immutable strategies and Runner records", async ({ page }) => {
  await installShell(page);
  await page.route("**/api/admin/strategy/ListStrategies", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        strategies: [
          {
            strategy_id: "momentum",
            name: "动量策略",
            kind: "coin_selection",
            manifest_yaml: "api_version: moox.strategy/v2\nkind: coin_selection",
            compiled_json: "{}",
            source_hash: "abc123",
            created_at: "2026-07-30T00:00:00Z"
          }
        ],
        total: 1,
        page: 1,
        page_size: 20
      }
    })
  );
  await page.goto("/#/strategy/overview");
  await expect(page.getByRole("heading", { name: "策略", exact: true })).toBeVisible();
  await expect(page.getByText("动量策略")).toBeVisible();
  await expect(page.getByText("声明式权重策略")).toBeVisible();
  await expect(page.getByText("绩效")).toHaveCount(0);
  await expect(page.getByText("状态迁移")).toHaveCount(0);
});

test("Runner detail renders StrategyResult and target-weight FULL targets", async ({ page }) => {
  await installShell(page);
  await page.route("**/api/admin/strategy/GetRunner", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        runner: {
          runner_id: "runner-paper",
          strategy_id: "momentum",
          space_id: "space-1",
          source_view_id: "ohlcv",
          frequency: "1m",
          logical_account_id: "logical-paper",
          status: "ENABLED",
          last_success_at: "2026-07-30T00:00:00Z"
        }
      }
    })
  );
  await page.route("**/api/admin/strategy/GetStrategy", route =>
    route.fulfill({ json: { ret_info: { code: 0 }, strategy: { strategy_id: "momentum", name: "动量策略" } } })
  );
  await page.route("**/api/admin/strategy/ListStrategyResults", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        results: [
          {
            result_id: "result-7",
            runner_id: "runner-paper",
            action: "rebalance",
            strategy_id: "momentum",
            period_time: "2026-07-30T00:00:00Z",
            input_hash: "hash7",
            debug_info_json: "{}",
            command_sequence: "7"
          }
        ],
        total: 1
      }
    })
  );
  await page.route("**/api/admin/strategy/ListStrategyTargets", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.25" }],
        command_sequence: "7"
      }
    })
  );
  await page.goto("/#/strategy/detail/runner-paper");
  await expect(page.getByRole("heading", { name: "runner-paper" })).toBeVisible();
  await expect(page.getByText("BTC-USDT-SPOT")).toBeVisible();
  await expect(page.getByText("0.25")).toBeVisible();
  await page.getByText("策略结果", { exact: true }).click();
  await expect(page.getByText("result-7")).toBeVisible();
  await expect(page.getByRole("button", { name: "停用" })).toBeVisible();
});

test("组合账户暂停时展示待执行目标和逐账户清仓结果", async ({ page }) => {
  await installShell(page);
  const logicalAccount = {
    logical_account_id: "logical-paper",
    name: "Paper 组合",
    execution_mode: 1,
    market_type: 1,
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    pause_reason: "manual review",
    owner_runner_id: "runner-paper",
    ready: true,
    readiness_reasons: [],
    members: [{ trading_account_id: "binance-paper", enabled: true, priority: 0 }]
  };
  await page.route("**/api/admin/trade_console/ListLogicalAccounts", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        logical_accounts: [logicalAccount],
        page_result: { page: 1, size: 20, total: 1 }
      }
    })
  );
  await page.route("**/api/admin/trade_console/GetLogicalAccount", route =>
    route.fulfill({ json: { ret_info: { code: 0 }, logical_account: logicalAccount } })
  );
  await page.route("**/api/admin/trade_console/GetLogicalAccountTarget", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        target: {
          target_id: "result-8",
          logical_account_id: "logical-paper",
          runner_id: "runner-paper",
          command_sequence: "8",
          targets: [{ instrument_id: "ETH-USDT-SPOT", quantity: "2" }],
          status: "ACKED"
        }
      }
    })
  );
  await page.route("**/api/admin/trade_console/FlattenLogicalAccount", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        action: {
          action_id: "flatten-1",
          logical_account_id: "logical-paper",
          action_type: "FLATTEN",
          reason: "risk cleanup",
          status: "PARTIAL",
          result_json: JSON.stringify({
            accounts: [
              {
                trading_account_id: "binance-paper",
                status: "PARTIAL",
                remaining_positions: [{ instrument_id: "ETH-USDT-SPOT", quantity: "0.2", reason: "minimum quantity" }],
                error: "minimum quantity"
              }
            ]
          })
        }
      }
    })
  );
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
