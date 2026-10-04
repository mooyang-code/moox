import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const tradingAccountListRequests: Record<string, unknown>[] = [];
const logicalAccountListRequests: Record<string, unknown>[] = [];
const orderListRequests: Record<string, unknown>[] = [];

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
const secondaryAccount = {
  ...account,
  trading_account_id: "ta-demo-2",
  name: "实盘主账户",
  execution_mode: 2,
  ready: false,
  last_error: "等待首次同步",
  paper: undefined,
  live: { environment: 2, credential_secret_id: "live-secret" }
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
      tradingAccountListRequests.push((route.request().postDataJSON?.() || {}) as Record<string, unknown>);
      return route.fulfill({
        json: ok({ accounts: [account, secondaryAccount], page_result: { page: 1, size: 200, total: 2 } })
      });
    case "ListLogicalAccounts":
      logicalAccountListRequests.push((route.request().postDataJSON?.() || {}) as Record<string, unknown>);
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
    case "ListHoldings": {
      const requestedAccountId = (route.request().postDataJSON?.() as { trading_account_id?: string } | undefined)
        ?.trading_account_id;
      const holdingsForAccount =
        requestedAccountId === secondaryAccount.trading_account_id
          ? [
              {
                ...account,
                trading_account_id: secondaryAccount.trading_account_id,
                asset: "SOL",
                instrument_id: "SOL-USDT-SPOT",
                exchange_symbol: "SOLUSDT",
                quantity: "12",
                average_cost: "140",
                mark_price: "150",
                market_value: "1800",
                unrealized_pnl: "120",
                source_time: "1700000000000"
              }
            ]
          : [
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
              },
              {
                ...account,
                asset: "ETH",
                instrument_id: "ETH-USDT-SPOT",
                exchange_symbol: "ETHUSDT",
                quantity: "1.2",
                average_cost: "2900",
                mark_price: "3000",
                market_value: "3600",
                unrealized_pnl: "120",
                source_time: "1700000000000"
              }
            ];
      return route.fulfill({ json: ok({ holdings: holdingsForAccount }) });
    }
    case "ListPositions":
      return route.fulfill({ json: ok({ positions: [] }) });
    case "ListOrders":
      orderListRequests.push((route.request().postDataJSON?.() || {}) as Record<string, unknown>);
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
  tradingAccountListRequests.length = 0;
  logicalAccountListRequests.length = 0;
  orderListRequests.length = 0;
  await installE2ESession(page, "space-1");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockTradeGateway);
});

test("工作台页签只加载当前视图并保留组合账户深链", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await expect(page.getByRole("tab", { name: "执行账户", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect.poll(() => tradingAccountListRequests.length).toBe(1);
  expect(logicalAccountListRequests).toHaveLength(0);

  await page.getByRole("tab", { name: "组合账户", exact: true }).click();
  await expect(page).toHaveURL(/trading\/accounts\?view=strategy/);
  await expect.poll(() => logicalAccountListRequests.length).toBe(1);
  await expect(page.getByRole("table").getByText("趋势组合", { exact: true })).toBeVisible();
  const logicalRequestCount = logicalAccountListRequests.length;

  await page.getByRole("tab", { name: "执行账户", exact: true }).click();
  await expect(page).toHaveURL(/trading\/accounts$/);
  await expect.poll(() => tradingAccountListRequests.length).toBe(2);
  expect(logicalAccountListRequests.length).toBe(logicalRequestCount);
});

test("组合账户工作台使用中文高密度表格", async ({ page }) => {
  await page.goto("/#/trading/accounts?view=strategy");
  await expect(page.getByRole("tab", { name: "组合账户", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByText("运行中", { exact: true }).first()).toBeVisible();
  await expect(page.getByText("趋势组合", { exact: true })).toBeVisible();
  await expect(page.getByRole("table").getByText("模拟", { exact: true })).toBeVisible();
  await expect(page.getByRole("table").getByText("就绪", { exact: true })).toBeVisible();
});

test("旧组合账户地址重定向到统一工作台并打开详情", async ({ page }) => {
  await page.goto("/#/trading/logical-accounts?logical_account_id=la-demo-1");
  await expect(page).toHaveURL(/trading\/accounts\?(?=.*view=strategy)(?=.*logical_account_id=la-demo-1)/);
  await expect(page.getByRole("tab", { name: "组合账户", exact: true })).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("heading", { name: "趋势组合", exact: true })).toBeVisible();
});

test("持仓显示账户摘要和中文现货字段", async ({ page }) => {
  await page.goto("/#/trading/positions?trading_account_id=ta-demo-1");
  await expect(page.getByRole("heading", { name: "持仓" })).toBeVisible();
  await expect(page.getByText("权益", { exact: true })).toBeVisible();
  await expect(page.getByText("可用资金", { exact: true })).toBeVisible();
  await expect(page.getByText("BTC-USDT-SPOT", { exact: true })).toBeVisible();
  await expect(page.getByText("ETH-USDT-SPOT", { exact: true })).toBeVisible();
  await expect(page.getByText("未实现盈亏", { exact: true }).first()).toBeVisible();
});

test("持仓切换账户后自动刷新并支持现货标的筛选", async ({ page }) => {
  let holdingsRequests = 0;
  page.on("request", request => {
    if (request.url().includes("/ListHoldings")) holdingsRequests += 1;
  });
  await page.goto("/#/trading/positions?trading_account_id=ta-demo-1");
  await expect(page.getByText("ETH-USDT-SPOT", { exact: true })).toBeVisible();
  await expect.poll(() => holdingsRequests).toBe(1);

  await page.locator(".position-filter-bar .arco-select-view").click();
  await page.getByText("实盘主账户 · 现货", { exact: true }).click();
  await expect(page).toHaveURL(/trading\/positions\?trading_account_id=ta-demo-2/);
  await expect(page.getByText("SOL-USDT-SPOT", { exact: true })).toBeVisible();
  await expect(page.getByText("ETH-USDT-SPOT", { exact: true })).toHaveCount(0);
  await expect.poll(() => holdingsRequests).toBe(2);

  await page.getByPlaceholder("交易标的").fill("btc");
  await page.getByRole("button", { name: "查询" }).click();
  await expect(page.getByText("暂无持仓数据", { exact: true })).toBeVisible();
  await expect.poll(() => holdingsRequests).toBe(3);
});

test("订单页合并订单与成交明细并支持状态筛选", async ({ page }) => {
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1");
  await expect(page.getByRole("heading", { name: "交易记录", exact: true })).toBeVisible();
  await expect(page.locator(".orders-page .page-title-tabs")).toHaveCount(0);
  await expect(page.getByPlaceholder("订单状态")).toBeVisible();
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();
  await page.locator(".orders-page .arco-select").nth(1).click();
  await page.getByText("已成交", { exact: true }).click();
  await page.getByRole("button", { name: "查询", exact: true }).click();
  await expect.poll(() => orderListRequests.at(-1)?.state).toBe("FILLED");
  await page.getByRole("button", { name: "详情", exact: true }).click();
  await expect(page.getByText("成交明细", { exact: true })).toBeVisible();
  await expect(page.getByText("成交编号", { exact: true })).toBeVisible();
  await expect(page.getByText("已实现盈亏", { exact: true })).toBeVisible();
  await expect(page.getByText("成交时间", { exact: true })).toBeVisible();
});

test("订单页成交深链归一到统一订单页面", async ({ page }) => {
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1&tab=fills");
  await expect(page).toHaveURL(/trading\/orders\?trading_account_id=ta-demo-1$/);
  await expect(page.getByRole("heading", { name: "交易记录", exact: true })).toBeVisible();
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();

  await page.evaluate(() => {
    window.location.hash = "#/trading/orders?trading_account_id=ta-demo-1&tab=fills";
  });
  await expect(page).toHaveURL(/trading\/orders\?trading_account_id=ta-demo-1$/);
});

test("订单详情只按需加载成交明细", async ({ page }) => {
  let orderRequests = 0;
  let fillRequests = 0;
  page.on("request", request => {
    if (request.url().includes("/ListOrders")) orderRequests += 1;
    if (request.url().includes("/ListFills")) fillRequests += 1;
  });
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1");
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();
  await expect.poll(() => orderRequests).toBe(1);
  expect(fillRequests).toBe(0);
  await page.getByRole("button", { name: "详情", exact: true }).click();
  await expect(page.getByText("成交编号", { exact: true })).toBeVisible();
  await expect.poll(() => fillRequests).toBe(1);
});

test("账户深链失效时关闭订单详情", async ({ page }) => {
  await page.goto("/#/trading/orders?trading_account_id=ta-demo-1");
  await expect(page.getByRole("table").getByText("挂单中", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "详情", exact: true }).click();
  await expect(page.getByText("订单编号", { exact: true })).toBeVisible();

  await page.evaluate(() => {
    window.location.hash = "#/trading/orders";
  });
  await expect(page).toHaveURL(/trading\/orders$/);
  await expect(page.getByText("订单编号", { exact: true })).toHaveCount(0);
  await expect(page.getByText("成交明细", { exact: true })).toHaveCount(0);
});

test("交易工作台窄屏不产生页面横向溢出", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  for (const route of [
    "/#/trading/accounts?view=strategy",
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
