import type { Page, Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

// 交易账户页的 mock 数据：所有接口都被拦截，只连本地 mock 数据，不会触达任何 Trade 节点。
const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const NOW = Date.now();

const snapshot = (equity: string, available: string) => ({
  balances: [],
  equity,
  available_funds: available,
  used_margin: "120",
  maintenance_margin: "0",
  unrealized_pnl: "-3.5",
  exchange_updated_at: String(NOW)
});
const baseAccount = {
  space_id: "space-1",
  settlement_asset: "USDT",
  margin_mode: "CROSS",
  status: "ENABLED",
  ready: true,
  leverage_settings: { "*": "1" },
  last_sync_at: String(NOW - 30_000),
  last_ready_at: String(NOW),
  last_error: "",
  created_at: "1",
  updated_at: "1"
};
const paperConfig = { initial_balance: "100000", maker_fee_rate: "0.0002", taker_fee_rate: "0.0005", slippage_bps: "1" };
const accounts = [
  {
    ...baseAccount,
    trading_account_id: "ta-paper-1",
    name: "币安模拟",
    exchange: 1,
    markets: [1, 2],
    execution_mode: 1,
    paper: paperConfig,
    snapshot: snapshot("100850.12", "98000")
  },
  {
    ...baseAccount,
    trading_account_id: "ta-paper-free",
    name: "OKX 模拟（未加入）",
    exchange: 2,
    markets: [2],
    execution_mode: 1,
    paper: paperConfig,
    snapshot: snapshot("5000", "5000")
  },
  {
    ...baseAccount,
    trading_account_id: "ta-testnet-1",
    name: "币安测试网",
    exchange: 1,
    markets: [2],
    execution_mode: 2,
    live: { environment: 1, credential_secret_id: "secret-testnet" },
    snapshot: snapshot("20000", "19000")
  },
  {
    ...baseAccount,
    trading_account_id: "ta-prod-1",
    name: "币安生产",
    exchange: 1,
    markets: [1, 2],
    execution_mode: 2,
    live: { environment: 2, credential_secret_id: "secret-prod" },
    snapshot: snapshot("50000", "42000")
  }
];
const logicals = [
  {
    logical_account_id: "la-paper-1",
    name: "默认策略模拟组合",
    control_mode: 1,
    execution_mode: 1,
    markets: [1, 2],
    settlement_asset: "USDT",
    automation_state: "ACTIVE",
    owner_instance_id: "inst-1",
    ready: true,
    readiness_reasons: [],
    members: [{ trading_account_id: "ta-paper-1", enabled: true, priority: 1 }]
  },
  {
    logical_account_id: "la-paper-manual",
    name: "手动模拟组合",
    control_mode: 2,
    execution_mode: 1,
    markets: [],
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    ready: false,
    readiness_reasons: ["没有启用的执行账户"],
    members: []
  },
  {
    logical_account_id: "la-testnet-1",
    name: "测试网组合",
    control_mode: 1,
    execution_mode: 2,
    markets: [2],
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    pause_reason: "手工检查",
    ready: true,
    readiness_reasons: [],
    members: [{ trading_account_id: "ta-testnet-1", enabled: true, priority: 1 }]
  },
  {
    logical_account_id: "la-prod-1",
    name: "生产组合",
    control_mode: 1,
    execution_mode: 2,
    markets: [1, 2],
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    ready: true,
    readiness_reasons: [],
    members: [{ trading_account_id: "ta-prod-1", enabled: true, priority: 1 }]
  }
];

const orders = [
  {
    order_id: "o-1",
    trading_account_id: "ta-paper-1",
    exchange: 1,
    market_type: 2,
    instrument_id: "BTC-USDT-SWAP",
    side: 1,
    order_type: 1,
    quantity: "0.5",
    filled_quantity: "0.5",
    average_price: "64000",
    reference_price: "64010",
    owner_type: "TARGET",
    state: "FILLED",
    submitted_at: String(NOW - 600_000),
    created_at: String(NOW - 600_000)
  },
  {
    order_id: "o-2",
    trading_account_id: "ta-paper-1",
    exchange: 1,
    market_type: 1,
    instrument_id: "ETH-USDT-SPOT",
    side: 2,
    order_type: 2,
    quantity: "2",
    filled_quantity: "0",
    average_price: "0",
    limit_price: "3500",
    reference_price: "3400",
    owner_type: "OPERATOR",
    state: "OPEN",
    submitted_at: String(NOW - 60_000),
    created_at: String(NOW - 60_000)
  }
];

export interface Calls {
  paper: Record<string, unknown>[];
  members: Record<string, unknown>[];
  flatten: Record<string, unknown>[];
  /** ListLogicalAccounts 被调用的次数 */
  listLogicals: number;
}
export let calls: Calls;

async function gateway(route: Route) {
  const method = route.request().url().split("/").pop()?.split("?")[0];
  const body = (route.request().postDataJSON?.() || {}) as Record<string, unknown>;
  switch (method) {
    case "GetUserInfo":
      return route.fulfill({
        json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
      });
    case "ListSpaces":
      return route.fulfill({
        json: ok({ spaces: [{ space_id: "space-1", name: "Crypto", owner: "e2e", status: "active" }], page_result: { total: 1 } })
      });
    case "ListTradingAccounts":
      return route.fulfill({ json: ok({ accounts, page_result: { page: 1, size: 200, total: accounts.length } }) });
    case "ListLogicalAccounts":
      calls.listLogicals += 1;
      return route.fulfill({
        json: ok({ logical_accounts: logicals, page_result: { page: 1, size: 500, total: logicals.length } })
      });
    case "GetLogicalAccount":
      return route.fulfill({
        json: ok({ logical_account: logicals.find(item => item.logical_account_id === body.logical_account_id) })
      });
    case "GetLogicalAccountTarget":
      return route.fulfill({
        json: ok({
          target: {
            target_id: "t-1",
            logical_account_id: body.logical_account_id,
            status: "CONVERGING",
            bar_end_time: NOW - 120_000,
            valid_until: NOW + 600_000,
            targets: [
              { instrument_id: "BTC-USDT-SWAP", quantity: "1" },
              { instrument_id: "BTC-USDT-SPOT", quantity: "0.2" },
              { instrument_id: "SOL-USDT-SWAP", quantity: "-30" }
            ],
            blocked_targets: [{ instrument_id: "SOL-USDT-SWAP", quantity: "-30", reason: "低于最小下单额" }]
          }
        })
      });
    case "ListPositions":
      return route.fulfill({
        json: ok({
          positions: [
            {
              trading_account_id: "ta-paper-1",
              market_type: 2,
              instrument_id: "BTC-USDT-SWAP",
              signed_quantity: "0.5",
              mark_price: "64000"
            },
            {
              trading_account_id: "ta-paper-1",
              market_type: 2,
              instrument_id: "DOGE-USDT-SWAP",
              signed_quantity: "-100",
              mark_price: "0.15"
            }
          ]
        })
      });
    case "ListHoldings":
      return route.fulfill({
        json: ok({
          holdings: [
            {
              trading_account_id: "ta-paper-1",
              asset: "BTC",
              instrument_id: "BTC-USDT-SPOT",
              quantity: "0.2",
              market_value: "12800"
            },
            { trading_account_id: "ta-paper-1", asset: "USDT", instrument_id: "", quantity: "88000", market_value: "88000" }
          ]
        })
      });
    case "QueryEquityCurve":
      return route.fulfill({
        json: ok({
          points: [
            { bucket_time: String(NOW - 80_000_000), equity: "100000" },
            { bucket_time: String(NOW - 1_000_000), equity: "100850.12" }
          ]
        })
      });
    case "ListOrders": {
      const matched = orders.filter(
        item => (!body.only_open || item.state === "OPEN") && (!body.owner_type || item.owner_type === body.owner_type)
      );
      return route.fulfill({
        json: ok({ orders: matched, page_result: { page: 1, size: 20, total: matched.length } })
      });
    }
    case "SummarizeAccountPnl":
      // 和后端一致：proto3 的零值不出现，funding_truncated 为 false 时不返回
      return route.fulfill({
        json: ok({
          fees: [
            { asset: "USDT", amount: "0.016" },
            { asset: "BNB", amount: "0.0004" }
          ],
          realized_pnl: [{ asset: "USDT", amount: "12.5" }],
          funding_fees: [{ asset: "USDT", amount: "-0.8" }],
          fill_count: "3",
          funding_available: true,
          funding_from: String(NOW - 7 * 86_400_000)
        })
      });
    case "ListOperatorActions":
      return route.fulfill({
        json: ok({
          actions: [
            {
              action_id: "act-2",
              logical_account_id: body.logical_account_id,
              action_type: "DELIST_FLATTEN",
              reason: "ABC 即将下架",
              status: "COMPLETED",
              result_json: '{"unblocked":true}',
              created_at: String(NOW - 120_000),
              updated_at: String(NOW - 60_000)
            },
            {
              action_id: "act-1",
              logical_account_id: body.logical_account_id,
              action_type: "REDUCE",
              reason: "保证金率过低",
              status: "FAILED",
              result_json: "",
              last_error: "撤单失败",
              created_at: String(NOW - 3_600_000),
              updated_at: String(NOW - 3_500_000)
            }
          ].filter(item => !body.action_type || item.action_type === body.action_type),
          page_result: { page: 1, size: 10, total: 2 }
        })
      });
    case "ListFills":
      return route.fulfill({
        json: ok({
          fills: [
            {
              fill_id: "f-1",
              order_id: "o-1",
              trading_account_id: "ta-paper-1",
              market_type: 2,
              instrument_id: "BTC-USDT-SWAP",
              side: 1,
              price: "64000",
              quantity: "0.5",
              fee: "0.016",
              fee_asset: "USDT",
              realized_pnl: "0",
              role: "TAKER",
              traded_at: String(NOW - 590_000)
            }
          ],
          page_result: { page: 1, size: 20, total: 1 }
        })
      });
    case "GetOrder":
      return route.fulfill({ json: ok({ order: orders[0] }) });
    case "ListAccountLedger":
      return route.fulfill({
        json: ok({
          entries: [
            { id: "l-1", time: String(NOW - 500_000), type: "FEE", asset: "USDT", amount: "-0.016", exchange_symbol: "BTCUSDT" }
          ],
          has_more: false
        })
      });
    case "ListStrategyInstances":
      return route.fulfill({
        json: ok({
          instances: [
            { instance_id: "inst-1", strategy_id: "st-1", logical_account_id: "la-paper-1", enabled: true, health: "ok" }
          ],
          total: 1
        })
      });
    case "ListStrategies":
      return route.fulfill({ json: ok({ strategies: [{ strategy_id: "st-1", name: "动量轮动" }], total: 1 }) });
    case "CreatePaperSimulation":
      calls.paper.push(body);
      return route.fulfill({
        json: ok({ account: accounts[0], logical_account: { ...logicals[1], logical_account_id: "la-new", name: "新模拟" } })
      });
    case "AddLogicalAccountMember":
      calls.members.push(body);
      return route.fulfill({ json: ok({ logical_account: logicals[0] }) });
    case "FlattenLogicalAccount":
      calls.flatten.push(body);
      return route.fulfill({
        json: ok({
          action: {
            action_id: body.action_id,
            logical_account_id: body.logical_account_id,
            action_type: "FLATTEN",
            reason: body.reason,
            status: "RUNNING",
            result_json: ""
          }
        })
      });
    case "GetOperatorAction":
      return route.fulfill({
        json: ok({
          action: {
            action_id: body.action_id,
            logical_account_id: "la-prod-1",
            action_type: "FLATTEN",
            status: "RUNNING",
            result_json: ""
          }
        })
      });
    case "GetExecutionCapabilities":
      return route.fulfill({
        json: ok({
          capabilities: {
            can_place_order: true,
            order_types: [1, 2],
            fill_policies: [1, 2, 3],
            can_close_paper_simulation: true,
            // 生产账户的交易所没有流水接口，用来测“不支持”的展示
            ledger: body.trading_account_id !== "ta-prod-1"
          }
        })
      });
    case "ListTradableInstruments":
      return route.fulfill({
        json: ok({
          instruments: [
            {
              instrument_id: "BTC-USDT-SPOT",
              quantity_step: "0.0001",
              min_quantity: "0.0001",
              min_notional: "5",
              price_tick: "0.01"
            },
            {
              instrument_id: "BTC-USDT-SWAP",
              quantity_step: "0.001",
              min_quantity: "0.001",
              min_notional: "5",
              price_tick: "0.1"
            }
          ]
        })
      });
    default:
      return route.fulfill({ json: ok() });
  }
}

export async function installTradingMock(page: Page) {
  calls = { paper: [], members: [], flatten: [], listLogicals: 0 };
  await installE2ESession(page, "space-1");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, gateway);
  return calls;
}
