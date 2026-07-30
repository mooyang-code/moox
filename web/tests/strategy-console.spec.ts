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
            manifest_yaml: "entrypoint: strategy",
            source_code: "def strategy(data, params, context): pass",
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
  await page.route("**/api/admin/strategy/GetEngineStatus", route =>
    route.fulfill({ json: { ret_info: { code: 0 }, workers: 1, ready_workers: 1 } })
  );
  await page.goto("/#/strategy/overview");
  await expect(page.getByRole("heading", { name: "策略", exact: true })).toBeVisible();
  await expect(page.getByText("动量策略")).toBeVisible();
  await expect(page.getByText("Worker 1/1")).toBeVisible();
  await expect(page.getByText("绩效")).toHaveCount(0);
  await expect(page.getByText("状态迁移")).toHaveCount(0);
});

test("Runner detail renders StrategyResult and quantity FULL targets", async ({ page }) => {
  await installShell(page);
  await page.route("**/api/admin/strategy/GetRunner", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        runner: {
          runner_id: "runner-paper",
          strategy_id: "momentum",
          view_id: "ohlcv",
          frequency: "1m",
          params_json: "{}",
          logical_account_id: "logical-paper",
          status: "enabled",
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
            namespace: "prod",
            input_hash: "hash7",
            output_json: "{}",
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
        targets: [{ instrument_id: "BTC-USDT-SPOT", quantity: "0.25" }],
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

test("paused Logical Account exposes pending FULL target and per-account flatten result", async ({ page }) => {
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
    members: [{ exchange_account_id: "binance-paper", enabled: true, priority: 0 }]
  };
  await page.route("**/api/admin/trade_logical_account/ListLogicalAccounts", route =>
    route.fulfill({
      json: {
        ret_info: { code: 0 },
        logical_accounts: [logicalAccount],
        page_result: { page: 1, size: 20, total: 1 }
      }
    })
  );
  await page.route("**/api/admin/trade_logical_account/GetLogicalAccount", route =>
    route.fulfill({ json: { ret_info: { code: 0 }, logical_account: logicalAccount } })
  );
  await page.route("**/api/admin/trade_execution/GetLogicalAccountTarget", route =>
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
  await page.route("**/api/admin/trade_logical_account/FlattenLogicalAccount", route =>
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
                exchange_account_id: "binance-paper",
                status: "PARTIAL",
                remaining_positions: [{ symbol: "ETH-USDT-SPOT", quantity: "0.2", reason: "minimum quantity" }],
                error: "minimum quantity"
              }
            ]
          })
        }
      }
    })
  );
  await page.goto("/#/trading/logical-accounts");
  await page.getByRole("button", { name: "管理" }).click();
  await expect(page.getByText("当前 FULL 目标 sequence 8 已保存但不会执行")).toBeVisible();
  await page.getByRole("button", { name: "逐账户清仓" }).click();
  const modal = page.locator(".arco-modal:visible").filter({ hasText: "逐账户清仓" });
  await modal.locator("input").nth(0).fill("flatten-1");
  await modal.locator("input").nth(1).fill("risk cleanup");
  await modal.getByRole("button", { name: "确定" }).click();
  await expect(page.getByText("0.2 (minimum quantity)")).toBeVisible();
  await expect(page.getByText("PARTIAL").first()).toBeVisible();
});
