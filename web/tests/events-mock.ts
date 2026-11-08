import type { Page, Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

// 事件中心的 mock：拦截 /api/admin/** 的全部调用，只用本地数据，不会触达事件中心服务。
const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const NOW = Date.now();
const DELIST = "event.collector.instrument.delist_scheduled";

const sample = {
  event_id: "test-示例",
  event: DELIST,
  version: 1,
  space_id: "mooxsys",
  subject: "test",
  occurred_at: "2026-11-07T08:00:00Z",
  rule_id: "示例规则",
  data: { exchange: "binance", market: "SPOT", exchange_symbol: "FOOUSDT", delist_at: "2026-11-11T08:00:00Z", scope: "ALL" }
};

const events = [
  {
    event_name: DELIST,
    version: 1,
    title: "标的即将下架",
    description: "交易所公布了某个标的的下架时间，或者手工登记了下架",
    scope_fields: [{ field: "exchange", title: "交易所", options: ["binance", "okx"] }],
    sample_json: JSON.stringify(sample),
    payload_fields: [
      { field: "exchange", title: "交易所：binance 或 okx" },
      { field: "market", title: "市场：SPOT 现货，SWAP 永续" },
      { field: "exchange_symbol", title: "交易所的标的代码" },
      { field: "delist_at", title: "下架时刻（UTC）" },
      { field: "scope", title: "影响范围：ALL 实盘和模拟盘，PAPER_ONLY 只影响模拟盘（演练用）" }
    ]
  }
];

const rules = [
  {
    rule_id: "sys",
    name: "卖出策略持有的下架币",
    event_name: DELIST,
    action_type: 2,
    system_action: 1,
    system: true,
    enabled: true,
    last_execution: { status: 3, at_ms: String(NOW - 3_600_000) }
  },
  {
    rule_id: "r1",
    name: "下架通知到交易群",
    event_name: DELIST,
    action_type: 1,
    notify_url: "https://example.com/hooks/moox",
    secret_hint: "ab12",
    enabled: true,
    last_execution: { status: 4, at_ms: String(NOW - 600_000) }
  }
];

const executions = [
  {
    execution_id: "x-failed",
    rule_id: "r1",
    rule_name: "下架通知到交易群",
    event_name: DELIST,
    subject_id: "binance:SPOT:FOOUSDT",
    status: 4,
    action_type: 1,
    attempts: 6,
    created_at_ms: String(NOW - 600_000),
    last_error: "对方返回 502"
  },
  {
    execution_id: "x-ok",
    rule_id: "sys",
    rule_name: "卖出策略持有的下架币",
    event_name: DELIST,
    subject_id: "binance:SPOT:FOOUSDT",
    status: 3,
    action_type: 2,
    attempts: 1,
    created_at_ms: String(NOW - 3_600_000)
  }
];

export interface EventCalls {
  bodies: Record<string, Record<string, unknown>[]>;
}
let calls: EventCalls;

async function gateway(route: Route) {
  const method = route.request().url().split("/").pop()?.split("?")[0] || "";
  const body = (route.request().postDataJSON?.() || {}) as Record<string, unknown>;
  (calls.bodies[method] ||= []).push(body);
  switch (method) {
    case "GetUserInfo":
      return route.fulfill({
        json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
      });
    case "ListSpaces":
      return route.fulfill({
        json: ok({
          spaces: [{ space_id: "crypto", name: "Crypto", owner: "e2e", status: "active", timezone: "UTC" }],
          page_result: { total: 1 }
        })
      });
    case "ListSubscribableEvents":
      return route.fulfill({ json: ok({ events, available_system_actions: [1] }) });
    case "ListEventRules":
      return route.fulfill({ json: ok({ rules, page_result: { page: 1, size: 200, total: rules.length, has_more: false } }) });
    case "ListEventExecutions": {
      const status = body.status === "EXECUTION_STATUS_FAILED" ? 4 : 0;
      const list = executions.filter(
        item => (!status || item.status === status) && (!body.rule_id || item.rule_id === body.rule_id)
      );
      return route.fulfill({ json: ok({ executions: list, page_result: { total: list.length } }) });
    }
    case "GetEventExecution": {
      const execution = executions.find(item => item.execution_id === body.execution_id) ?? executions[0];
      return route.fulfill({
        json: ok({
          detail: {
            execution,
            payload_json: JSON.stringify(sample.data),
            attempts: [
              { attempt: 1, at_ms: String(NOW - 600_000), http_status: 502, duration_ms: 120, response_snippet: "bad gateway" }
            ],
            delist_steps: []
          }
        })
      });
    }
    default:
      return route.fulfill({ json: ok() });
  }
}

export async function installEventsMock(page: Page) {
  calls = { bodies: {} };
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, gateway);
  return calls;
}
