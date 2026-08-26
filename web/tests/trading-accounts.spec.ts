import { expect, test, type Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const paperRequests: Record<string, unknown>[] = [];
const liveRequests: Record<string, unknown>[] = [];
const closeRequests: Record<string, unknown>[] = [];

const paperAccount = {
  trading_account_id: "ta-paper-1",
  space_id: "space-1",
  name: "Paper Demo",
  exchange: 1,
  market_type: 1,
  execution_mode: 1,
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: true,
  leverage_settings: {},
  sync_symbols: [],
  paper: { initial_balance: "100000", maker_fee_rate: "0", taker_fee_rate: "0", slippage_bps: "0" },
  snapshot: {
    balances: [],
    equity: "100000",
    available_funds: "100000",
    used_margin: "0",
    maintenance_margin: "0",
    unrealized_pnl: "0",
    exchange_updated_at: "1"
  },
  last_sync_at: "1",
  last_ready_at: "1",
  last_error: "",
  created_at: "1",
  updated_at: "1"
};

const { paper: _paperConfig, ...liveBase } = paperAccount;
const liveAccount = {
  ...liveBase,
  trading_account_id: "ta-live-1",
  name: "Live Testnet",
  execution_mode: 2,
  ready: false,
  live: { environment: 1, credential_secret_id: "secret-1" },
  sync_symbols: ["BTCUSDT"],
  last_error: "symbol not found"
};

async function mockTradeGateway(route: Route) {
  const method = route.request().url().split("/").pop();
  if (method === "GetUserInfo") {
    return route.fulfill({
      json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
    });
  }
  if (method === "ListSpaces") {
    return route.fulfill({
      json: ok({
        spaces: [{ space_id: "space-1", name: "Crypto", owner: "e2e", status: "active" }],
        page_result: { page: 1, size: 20, total: 1 }
      })
    });
  }
  if (method === "ListTradingAccounts") {
    return route.fulfill({ json: ok({ accounts: [paperAccount, liveAccount], page_result: { page: 1, size: 20, total: 2 } }) });
  }
  if (method === "GetExecutionCapabilities") {
    return route.fulfill({
      json: ok({
        capabilities: {
          can_place_order: false,
          unavailable_reason: "not ready",
          order_types: [],
          fill_policies: [],
          can_close_paper_simulation: true
        }
      })
    });
  }
  if (method === "SyncTradingAccount") {
    return route.fulfill({
      json: ok({ fills_ingested: 1, orders_updated: 2, positions_updated: 1, ready: false, warnings: ["symbol not found"] })
    });
  }
  if (method === "CreatePaperSimulation") {
    paperRequests.push(route.request().postDataJSON?.() || {});
    return route.fulfill({
      json: ok({
        account: paperAccount,
        logical_account: { logical_account_id: "la-paper-1", name: "Paper Logical", members: [] }
      })
    });
  }
  if (method === "CreateTradingAccount") {
    liveRequests.push(route.request().postDataJSON?.() || {});
    return route.fulfill({ json: ok({ account: liveAccount }) });
  }
  if (method === "ClosePaperSimulation") {
    closeRequests.push(route.request().postDataJSON?.() || {});
    return route.fulfill({
      json: ok({
        account: { ...paperAccount, status: "CLOSED" },
        logical_account: { logical_account_id: "la-paper-1", name: "Paper Logical", members: [] }
      })
    });
  }
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  paperRequests.length = 0;
  liveRequests.length = 0;
  closeRequests.length = 0;
  await installE2ESession(page, "space-1");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockTradeGateway);
});

test("renders Paper and Live accounts and keeps their fields isolated", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await expect(page.getByRole("heading", { name: "交易账户" })).toBeVisible();
  await expect(page.getByText("Paper Demo", { exact: true })).toBeVisible();
  await expect(page.getByText("Live Testnet", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "创建账户" }).click();
  await expect(page.getByText("初始资金", { exact: true })).toBeVisible();
  await expect(page.getByText("Live Secret ID", { exact: true })).toHaveCount(0);
  await page.getByText("Live", { exact: true }).last().click();
  await expect(page.getByText("Live Secret ID", { exact: true })).toBeVisible();
  await expect(page.getByText("交易标的", { exact: true })).toBeVisible();
});

test("surfaces sync warnings and canonical navigation without real orders", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "同步" }).last().click();
  await expect(page.getByText("symbol not found", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "详情" }).last().click();
  await page.getByRole("button", { name: "查看持仓" }).click();
  await expect(page).toHaveURL(/trading\/positions.*trading_account_id=ta-live-1/);
  await page.goBack();
  await page.getByRole("button", { name: "详情" }).last().click();
  await page.getByRole("button", { name: "查看订单" }).click();
  await expect(page).toHaveURL(/trading\/orders.*trading_account_id=ta-live-1/);
});

test("requires Production confirmation and exposes the Paper close capability", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "创建账户" }).click();
  await page.getByText("Live", { exact: true }).last().click();
  await page.getByText("Production", { exact: true }).click();
  await page.getByRole("button", { name: "创建 Production 账户" }).click();
  await expect(page.getByText("确认创建 Production 账户", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  const createModal = page.locator(".arco-modal").filter({ hasText: "创建 Live 账户" });
  await createModal.getByRole("button", { name: "取消" }).click({ force: true });
  await expect(createModal).toBeHidden();

  await page.getByRole("button", { name: "详情" }).first().click();
  await expect(page.getByRole("button", { name: "关闭 Paper 模拟" })).toBeEnabled();
});

test("submits a Paper request without Live-only fields and shows both IDs", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "创建账户" }).click();
  await page.locator('input[name="account_name"]').fill("Paper Created");
  await page.locator(".arco-modal").getByRole("button", { name: "创建账户" }).click();
  await expect.poll(() => paperRequests.length).toBe(1);
  expect(paperRequests[0]).toMatchObject({ account_name: "Paper Created", settlement_asset: "USDT" });
  expect(paperRequests[0]).not.toHaveProperty("live");
  expect(paperRequests[0]).not.toHaveProperty("credential_secret_id");
  expect(paperRequests[0]).not.toHaveProperty("sync_symbols");
  await expect(page.getByText("la-paper-1", { exact: false })).toBeVisible();
});

test("submits Live configuration, syncs it, and shows readiness feedback", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "创建账户" }).click();
  await page.getByText("Live", { exact: true }).last().click();
  await page.locator('input[name="account_name"]').fill("Live Created");
  await page.locator('input[name="credential_secret_id"]').fill("secret-live");
  await page.locator('input[name="sync_symbols"]').fill("BTCUSDT");
  await page.locator(".arco-modal").getByRole("button", { name: "创建账户" }).click();
  await expect.poll(() => liveRequests.length).toBe(1);
  expect(liveRequests[0]).toMatchObject({
    name: "Live Created",
    sync_symbols: ["BTCUSDT"],
    live: { environment: 1, credential_secret_id: "secret-live" }
  });
  expect(liveRequests[0]).not.toHaveProperty("initial_balance");
  await expect(page.getByText("Live 账户已创建", { exact: false })).toBeVisible();
  await expect(page.getByText("Not Ready", { exact: false }).last()).toBeVisible();
});

test("closes Paper only after capability confirmation and sends canonical ID", async ({ page }) => {
  await page.goto("/#/trading/accounts");
  await page.getByRole("button", { name: "详情" }).first().click();
  await page.getByRole("button", { name: "关闭 Paper 模拟" }).click();
  await expect(page.getByText("关闭 Paper 模拟", { exact: true }).last()).toBeVisible();
  await page.getByRole("button", { name: "确定" }).last().click();
  await expect.poll(() => closeRequests.length).toBe(1);
  expect(closeRequests[0]).toEqual({ trading_account_id: "ta-paper-1" });
});

test("rejects invalid account deep links instead of querying an unknown account", async ({ page }) => {
  await page.goto("/#/trading/positions?trading_account_id=missing");
  await expect(page).not.toHaveURL(/trading_account_id=missing/);
  await expect(page.getByText("账户不存在或无权限", { exact: true }).last()).toBeVisible();

  await page.goto("/#/trading/orders?trading_account_id=missing");
  await expect(page).not.toHaveURL(/trading_account_id=missing/);
  await expect(page.getByText("账户不存在或无权限", { exact: true }).last()).toBeVisible();
});
