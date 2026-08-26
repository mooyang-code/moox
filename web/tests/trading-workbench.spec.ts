import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });

const account = {
  trading_account_id: "ta-demo-1",
  space_id: "space-1",
  name: "模拟账户",
  exchange: 1,
  market_type: 1,
  execution_mode: 1,
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: true,
  leverage_settings: {},
  sync_symbols: ["BTCUSDT"],
  snapshot: {
    balances: [],
    equity: "100240.20",
    available_funds: "82410.00",
    used_margin: "0",
    maintenance_margin: "0",
    unrealized_pnl: "240.20",
    exchange_updated_at: "1700000000000"
  },
  last_sync_at: "1700000000000",
  last_ready_at: "1700000000000",
  last_error: "",
  created_at: "1700000000000",
  updated_at: "1700000000000",
  paper: { initial_balance: "100000", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0" }
};

const logicalAccount = {
  logical_account_id: "la-demo-1",
  space_id: "space-1",
  name: "趋势组合",
  owner_runner_id: "runner-1",
  execution_mode: 1,
  market_type: 1,
  settlement_asset: "USDT",
  automation_state: "ACTIVE",
  pause_reason: "",
  members: [{ trading_account_id: "ta-demo-1", enabled: true, priority: 1 }],
  ready: true,
  readiness_reasons: [],
  created_at: "1700000000000",
  updated_at: "1700000000000"
};

async function mockTradeGateway(route: Route) {
  const method = route.request().url().split("/").pop()?.split("?")[0];
  switch (method) {
    case "GetUserInfo":
      return route.fulfill({
        json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
      });
    case "ListTradingAccounts":
      return route.fulfill({ json: ok({ accounts: [account], page_result: { page: 1, size: 200, total: 1 } }) });
    case "ListLogicalAccounts":
      return route.fulfill({ json: ok({ logical_accounts: [logicalAccount], page_result: { page: 1, size: 20, total: 1 } }) });
    case "GetLogicalAccount":
      return route.fulfill({ json: ok({ logical_account: logicalAccount }) });
    case "GetLogicalAccountTarget":
      return route.fulfill({
        json: ok({
          target: {
            target_id: "target-1",
            logical_account_id: "la-demo-1",
            runner_id: "runner-1",
            command_sequence: "7",
            targets: [{ instrument_id: "BTC-USDT-SPOT", quantity: "0.1" }],
            status: "SUCCEEDED",
            blocked_targets: [],
            last_error: "",
            accepted_at: "1700000000000",
            updated_at: "1700000000000"
          }
        })
      });
    case "ListHoldings":
      return route.fulfill({
        json: ok({
          holdings: [
            {
              ...account,
              asset: "BTC",
              instrument_id: "BTC-USDT-SPOT",
              exchange_symbol: "BTCUSDT",
              quantity: "0.1",
              average_cost: "65000",
              mark_price: "66000",
              market_value: "6600",
              unrealized_pnl: "100",
              source_time: "1700000000000"
            }
          ]
        })
      });
    case "ListPositions":
      return route.fulfill({ json: ok({ positions: [] }) });
    case "ListOrders":
      return route.fulfill({
        json: ok({
          orders: [
            {
              order_id: "order-1",
              trading_account_id: "ta-demo-1",
              exchange: 1,
              market_type: 1,
              instrument_id: "BTC-USDT-SPOT",
              order_type: 2,
              side: 1,
              quantity: "0.1",
              filled_quantity: "0",
              average_price: "-",
              limit_price: "65000",
              state: "OPEN",
              submitted_at: "1700000000000",
              created_at: "1700000000000"
            }
          ],
          page_result: { page: 1, size: 20, total: 1 }
        })
      });
    case "ListFills":
      return route.fulfill({
        json: ok({
          fills: [
            {
              fill_id: "fill-1",
              trading_account_id: "ta-demo-1",
              exchange: 1,
              market_type: 1,
              instrument_id: "BTC-USDT-SPOT",
              side: 1,
              price: "65000",
              quantity: "0.1",
              fee: "0.1",
              fee_asset: "USDT",
              realized_pnl: "12",
              role: "MAKER",
              traded_at: "1700000000000"
            }
          ],
          page_result: { page: 1, size: 20, total: 1 }
        })
      });
    default:
      return route.fulfill({ json: ok() });
  }
}

test.beforeEach(async ({ page }) => {
  await installE2ESession(page, "space-1");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockTradeGateway);
});

test("逻辑账户使用中文高密度表格", async ({ page }) => {
  await page.goto("/#/trading/logical-accounts");
  await expect(page.getByRole("heading", { name: "逻辑账户" })).toBeVisible();
  await expect(page.getByText("运行中", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("趋势组合", { exact: true })).toBeVisible();
  await expect(page.getByRole("table").getByText("模拟", { exact: true })).toBeVisible();
  await expect(page.getByRole("table").getByText("就绪", { exact: true })).toBeVisible();
});

test("持仓显示账户摘要和中文现货字段", async ({ page }) => {
  await page.goto("/#/trading/positions?trading_account_id=ta-demo-1");
  await expect(page.getByRole("heading", { name: "持仓" })).toBeVisible();
  await expect(page.getByText("权益", { exact: true })).toBeVisible();
  await expect(page.getByText("可用资金", { exact: true })).toBeVisible();
  await expect(page.getByText("BTC-USDT-SPOT", { exact: true })).toBeVisible();
  await expect(page.getByText("未实现盈亏", { exact: true }).first()).toBeVisible();
});

test("订单页支持中文订单和成交视图", async ({ page }) => {
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1");
  await expect(page.getByRole("tab", { name: "订单", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "订单", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".orders-page .arco-tabs-tab")).toHaveCount(0);
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();
  await page.getByRole("tab", { name: "成交", exact: true }).click();
  await expect(page).toHaveURL(/trading\/orders\?trading_account_id=ta-demo-1&tab=fills/);
  await expect(page.getByText("成交编号", { exact: true })).toBeVisible();
  await expect(page.getByText("已实现盈亏", { exact: true })).toBeVisible();
  await expect(page.getByText("挂单方", { exact: true })).toBeVisible();
});

test("订单页支持成交页签深链并保留账户参数", async ({ page }) => {
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1&tab=fills");
  await expect(page.getByRole("tab", { name: "成交", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("成交编号", { exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "订单", exact: true })).toHaveAttribute("aria-selected", "false");
});

test("订单页切换页签只发起一次目标数据请求", async ({ page }) => {
  let orderRequests = 0;
  let fillRequests = 0;
  page.on("request", request => {
    if (request.url().includes("/ListOrders")) orderRequests += 1;
    if (request.url().includes("/ListFills")) fillRequests += 1;
  });
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1");
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();
  await expect.poll(() => orderRequests).toBe(1);
  await page.getByRole("tab", { name: "成交", exact: true }).click();
  await expect(page.getByText("成交编号", { exact: true })).toBeVisible();
  await expect.poll(() => fillRequests).toBe(1);
});

test("交易工作台窄屏不产生页面横向溢出", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const route of [
    "/#/trading/logical-accounts",
    "/#/trading/positions?trading_account_id=ta-demo-1",
    "/#/trading/orders?trading_account_id=ta-demo-1"
  ]) {
    await page.goto(route);
    const metrics = await page.evaluate(() => ({
      clientWidth: document.documentElement.clientWidth,
      scrollWidth: document.documentElement.scrollWidth
    }));
    expect(metrics.scrollWidth, route).toBeLessThanOrEqual(metrics.clientWidth);
  }
});
