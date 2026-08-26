import fs from "node:fs";
import path from "node:path";
import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listTradingAccounts: vi.fn(),
  getExecutionCapabilities: vi.fn(),
  syncTradingAccount: vi.fn(),
  routerPush: vi.fn()
}));

vi.mock("@/api/trade", async () => {
  const actual = await vi.importActual<typeof import("@/api/trade")>("@/api/trade");
  return {
    ...actual,
    listTradingAccounts: mocks.listTradingAccounts,
    getExecutionCapabilities: mocks.getExecutionCapabilities,
    syncTradingAccount: mocks.syncTradingAccount
  };
});
vi.mock("vue-router", () => ({ useRouter: () => ({ push: mocks.routerPush }) }));

import AccountOverview from "./account-overview.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn().mockImplementation(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn()
  }))
});

const account = {
  trading_account_id: "ta-live-1",
  space_id: "space-1",
  name: "Live Testnet",
  exchange: 1,
  market_type: 1,
  execution_mode: 2,
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: false,
  leverage_settings: {},
  sync_symbols: ["BTCUSDT"],
  live: { environment: 1, credential_secret_id: "secret-1" },
  snapshot: {
    balances: [],
    equity: "100",
    available_funds: "90",
    used_margin: "10",
    maintenance_margin: "1",
    unrealized_pnl: "-2",
    exchange_updated_at: "1"
  },
  last_sync_at: "1",
  last_ready_at: "",
  last_error: "secret unavailable",
  created_at: "1",
  updated_at: "1"
};

describe("trading account workbench", () => {
  beforeEach(() => {
    mocks.listTradingAccounts.mockReset().mockResolvedValue({
      accounts: [account],
      page_result: { page: 1, size: 20, total: 1 }
    });
    mocks.getExecutionCapabilities.mockReset().mockResolvedValue({
      capabilities: {
        can_place_order: false,
        unavailable_reason: "not ready",
        order_types: [],
        fill_policies: [],
        can_close_paper_simulation: false
      }
    });
    mocks.syncTradingAccount.mockReset().mockResolvedValue({
      fills_ingested: 1,
      orders_updated: 2,
      positions_updated: 1,
      ready: false,
      warnings: ["symbol not found"]
    });
    mocks.routerPush.mockReset();
  });

  it("renders server readiness, snapshot and last error", async () => {
    const wrapper = mount(AccountOverview, {
      global: {
        plugins: [ArcoVue],
        stubs: { "icon-refresh": true, "icon-plus": true, "icon-sync": true }
      }
    });
    await flushPromises();
    expect(mocks.listTradingAccounts).toHaveBeenCalledWith({ page: { page: 1, size: 20 } });
    expect(wrapper.text()).toContain("未就绪");
    expect(wrapper.text()).toContain("100");
    expect(wrapper.text()).toContain("secret unavailable");
  });

  it("keeps the account page on canonical IDs and exposes explicit navigation", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "account-overview.vue"), "utf8");
    expect(source).toContain("query: { trading_account_id: account.trading_account_id }");
    expect(source).toContain("query: { logical_account_id: createdLogicalAccountId.value }");
    expect(source).not.toContain("exchange_account_id");
  });

  it("keeps creation reset, Paper capability gating and sync refresh in the component contract", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "account-overview.vue"), "utf8");
    expect(source).toContain("Object.assign(form, createDefaultAccountForm())");
    expect(source).toContain("createPaperSimulation(buildPaperSimulationRequest(form))");
    expect(source).toContain("getExecutionCapabilities(account.trading_account_id)");
    expect(source).toContain("getTradingAccount(account.trading_account_id)");
    expect(source).toContain("const syncRequests = createLatestRequestGuard()");
    expect(source).toContain("can_close_paper_simulation");
  });

  it("uses compact Chinese table headers and balanced column widths", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "account-overview.vue"), "utf8");
    expect(source).toContain(':scroll="{ x: 1400 }"');
    expect(source).toContain('<a-table-column title="交易所/市场" :width="120">');
    expect(source).toContain('<a-table-column title="执行配置" :width="140">');
    expect(source).toContain('<a-table-column title="资金" :width="180">');
    expect(source).toContain('<a-table-column title="错误" :width="200" ellipsis>');
    expect(source).not.toContain('title="Exchange / 市场"');
  });
});
