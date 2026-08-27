import fs from "node:fs";
import path from "node:path";
import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listTradingAccounts: vi.fn(),
  listHoldings: vi.fn(),
  listPositions: vi.fn(),
  syncTradingAccount: vi.fn(),
  routerReplace: vi.fn(),
  routeQuery: { trading_account_id: "ta-demo-1" } as Record<string, unknown>
}));

vi.mock("@/api/trade", async () => {
  const actual = await vi.importActual<typeof import("@/api/trade")>("@/api/trade");
  return {
    ...actual,
    listTradingAccounts: mocks.listTradingAccounts,
    listHoldings: mocks.listHoldings,
    listPositions: mocks.listPositions,
    syncTradingAccount: mocks.syncTradingAccount
  };
});
vi.mock("vue-router", () => ({
  useRouter: () => ({ replace: mocks.routerReplace }),
  useRoute: () => ({ query: mocks.routeQuery })
}));

import PositionDetail from "./position-detail.vue";

const account = {
  trading_account_id: "ta-demo-1",
  space_id: "space-1",
  name: "趋势模拟账户",
  exchange: 1,
  market_type: 1,
  execution_mode: 1,
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: true,
  leverage_settings: {},
  sync_symbols: ["BTCUSDT", "ETHUSDT"],
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

const holdings = [
  {
    trading_account_id: account.trading_account_id,
    instrument_id: "BTC-USDT-SPOT",
    exchange_symbol: "BTCUSDT",
    asset: "BTC",
    quantity: "0.1",
    average_cost: "65000",
    mark_price: "66000",
    market_value: "6600",
    unrealized_pnl: "100",
    source_time: "1700000000000"
  },
  {
    trading_account_id: account.trading_account_id,
    instrument_id: "ETH-USDT-SPOT",
    exchange_symbol: "ETHUSDT",
    asset: "ETH",
    quantity: "1.2",
    average_cost: "2900",
    mark_price: "3000",
    market_value: "3600",
    unrealized_pnl: "120",
    source_time: "1700000000000"
  }
];

describe("持仓页面", () => {
  beforeEach(() => {
    mocks.listTradingAccounts.mockReset().mockResolvedValue({ accounts: [account] });
    mocks.listHoldings.mockReset().mockResolvedValue({ holdings });
    mocks.listPositions.mockReset().mockResolvedValue({ positions: [] });
    mocks.syncTradingAccount.mockReset().mockResolvedValue({ positions_updated: 2 });
    mocks.routerReplace.mockReset();
    mocks.routeQuery = { trading_account_id: "ta-demo-1" };
  });

  it("renders one account context row and keeps action/filter controls in the page structure", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "position-detail.vue"), "utf8");
    expect(source).toContain('class="account-context-row"');
    expect(source).toContain('class="position-filter-bar"');
    expect(source).toContain('class="position-table-region"');
    expect(source).toContain('class="position-empty-state"');
  });

  it("filters spot holdings by instrument when querying", async () => {
    const wrapper = mount(PositionDetail, {
      global: {
        plugins: [ArcoVue],
        stubs: { "icon-refresh": true, "icon-sync": true, "icon-search": true }
      }
    });
    await flushPromises();
    expect(wrapper.text()).toContain("ETH-USDT-SPOT");

    await wrapper.find('input[placeholder="交易标的"]').setValue("btc");
    const queryButton = wrapper.findAll("button").find(button => button.text().includes("查询"));
    expect(queryButton).toBeDefined();
    await queryButton?.trigger("click");
    await flushPromises();

    expect(wrapper.text()).toContain("BTC-USDT-SPOT");
    expect(wrapper.text()).not.toContain("ETH-USDT-SPOT");
    expect(mocks.listHoldings).toHaveBeenCalledTimes(2);
  });
});
