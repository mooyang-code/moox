import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { EventRule, SubscribableEvent } from "@/api/callback-dispatcher";

const mocks = vi.hoisted(() => ({
  updateEventRule: vi.fn(),
  createEventRule: vi.fn(),
  listEventRules: vi.fn(),
  listAllEventRules: vi.fn(),
  listSubscribableEvents: vi.fn(),
  getEventExecution: vi.fn(),
  deleteEventRule: vi.fn(),
  listEventExecutions: vi.fn(),
  cancelEventExecution: vi.fn()
}));
vi.mock("@/api/callback-dispatcher", async () => ({
  ...(await vi.importActual<typeof import("@/api/callback-dispatcher")>("@/api/callback-dispatcher")),
  ...mocks
}));
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn(), callControl: vi.fn() }));

import RuleDialog from "./rule-dialog.vue";
import EventRules from "./rules.vue";
import ExecutionDrawer from "./execution-drawer.vue";
import EventExecutions from "./executions.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const events: SubscribableEvent[] = [
  {
    event_name: "event.collector.instrument.delist_scheduled",
    version: 1,
    title: "标的即将下架",
    description: "",
    scope_fields: [{ field: "exchange", title: "交易所", options: ["binance", "okx"] }]
  }
];

const rule = (patch: Partial<EventRule> = {}): EventRule => ({
  rule_id: "r1",
  name: "下架通知",
  event_name: "event.collector.instrument.delist_scheduled",
  scope: [{ field: "exchange", values: ["binance"] }],
  action_type: "ACTION_TYPE_NOTIFY_URL",
  notify_url: "https://example.com/hook",
  allow_private_address: false,
  secret_hint: "ab12",
  system_action: "SYSTEM_ACTION_UNSPECIFIED",
  system: false,
  enabled: true,
  created_at_ms: 0,
  updated_at_ms: 0,
  ...patch
});

let wrapper: VueWrapper | undefined;
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  document.body.innerHTML = "";
});
beforeEach(() => {
  for (const mock of Object.values(mocks)) mock.mockReset();
});

function okButton() {
  return [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(item => item.textContent?.includes("确定"));
}

describe("编辑规则", () => {
  async function openDialog(props: { rule: EventRule | null; events: SubscribableEvent[] }) {
    wrapper = mount(RuleDialog, { props: { visible: false, ...props }, global: { plugins: [ArcoVue] }, attachTo: document.body });
    await wrapper.setProps({ visible: true });
    await flushPromises();
    return wrapper;
  }

  it("没读到事件目录时禁用保存，不会把已有的范围清掉", async () => {
    await openDialog({ rule: rule(), events: [] });
    expect(document.body.textContent).toContain("没有读到事件目录");
    expect(okButton()?.disabled).toBe(true);
    expect(mocks.updateEventRule).not.toHaveBeenCalled();
  });

  it("只改名称时不带范围；改了范围才带", async () => {
    mocks.updateEventRule.mockResolvedValue(rule());
    const dialog = await openDialog({ rule: rule(), events });
    const vm = dialog.vm as unknown as { submit(): Promise<boolean>; scopeValues: Record<string, string[]> };
    await vm.submit();
    expect(mocks.updateEventRule.mock.calls[0][0]).not.toHaveProperty("scope");

    // 改了范围再保存一次
    vm.scopeValues.exchange = ["okx"];
    await vm.submit();
    expect(mocks.updateEventRule.mock.calls[1][0].scope).toEqual({ items: [{ field: "exchange", values: ["okx"] }] });
  });
});

describe("规则列表", () => {
  it("停用系统规则先弹窗说明后果，确认后才停用", async () => {
    const system = rule({ rule_id: "sys", name: "卖出下架币", system: true, action_type: "ACTION_TYPE_SYSTEM" });
    mocks.listEventRules.mockResolvedValue({ rules: [system], page_result: { total: 1 } });
    mocks.listAllEventRules.mockResolvedValue([system]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.updateEventRule.mockResolvedValue({ ...system, enabled: false });
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue] }, attachTo: document.body });
    await flushPromises();
    (wrapper.vm as unknown as { requestToggle(rule: EventRule, enabled: boolean): void }).requestToggle(system, false);
    await flushPromises();
    expect(document.body.textContent).toContain("之后发现的下架不会再处理");
    expect(mocks.updateEventRule).not.toHaveBeenCalled();
    const confirm = [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(
      item => item.textContent?.trim() === "停用"
    );
    confirm!.click();
    await flushPromises();
    expect(mocks.updateEventRule).toHaveBeenCalledWith({ rule_id: "sys", enabled: false });
  });

  it("系统规则的状态从全部规则里取，不只看当前页", async () => {
    const system = rule({ rule_id: "sys", system: true, enabled: false, action_type: "ACTION_TYPE_SYSTEM" });
    mocks.listEventRules.mockResolvedValue({ rules: [rule({ rule_id: "x" })], page_result: { total: 2 } });
    mocks.listAllEventRules.mockResolvedValue([system, rule({ rule_id: "x" })]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue] } });
    await flushPromises();
    expect((wrapper.vm as unknown as { systemRuleEnabled: boolean | null }).systemRuleEnabled).toBe(false);
  });
});

describe("规则列表的翻页与刷新", () => {
  it("删掉最后一页仅有的一行后回到上一页", async () => {
    const first = Array.from({ length: 10 }, (_, i) => rule({ rule_id: `r${i}` }));
    const last = rule({ rule_id: "r10" });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.listEventRules.mockImplementation(async ({ page }: { page: number }) =>
      page === 2 ? { rules: [last], page_result: { total: 11 } } : { rules: first, page_result: { total: 11 } }
    );
    mocks.deleteEventRule.mockResolvedValue({});
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue] } });
    await flushPromises();
    const vm = wrapper.vm as unknown as {
      pagination: { current: number };
      onPageChange(page: number): void;
      remove(r: EventRule): Promise<void>;
    };
    vm.onPageChange(2);
    await flushPromises();
    expect(vm.pagination.current).toBe(2);
    mocks.listEventRules.mockImplementation(async ({ page }: { page: number }) =>
      page === 2 ? { rules: [], page_result: { total: 10 } } : { rules: first, page_result: { total: 10 } }
    );
    await vm.remove(last);
    await flushPromises();
    expect(vm.pagination.current).toBe(1);
    expect((wrapper.vm as unknown as { rows: EventRule[] }).rows).toHaveLength(10);
  });

  it("迟到的旧页响应被丢弃", async () => {
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    let resolveOld!: (value: unknown) => void;
    mocks.listEventRules.mockImplementationOnce(() => new Promise(done => (resolveOld = done)));
    mocks.listEventRules.mockResolvedValue({ rules: [rule({ rule_id: "fresh" })], page_result: { total: 1 } });
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue] } });
    await flushPromises();
    (wrapper.vm as unknown as { refresh(): void }).refresh();
    await flushPromises();
    resolveOld({ rules: [rule({ rule_id: "stale" })], page_result: { total: 1 } });
    await flushPromises();
    expect((wrapper.vm as unknown as { rows: EventRule[] }).rows.map(item => item.rule_id)).toEqual(["fresh"]);
  });

  it("系统规则状态读不到时显示无法确认，不当成已启用", async () => {
    mocks.listEventRules.mockResolvedValue({ rules: [], page_result: { total: 0 } });
    mocks.listAllEventRules.mockRejectedValue(new Error("读不到"));
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: ["SYSTEM_ACTION_SELL_DELISTED_HOLDINGS"] });
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue] }, attachTo: document.body });
    await flushPromises();
    (wrapper.vm as unknown as { openDelist(): void }).openDelist();
    await flushPromises();
    expect(document.body.textContent).toContain("无法确认系统规则");
    expect(document.body.textContent).not.toContain("登记后，系统会立即禁止买入这个标的");
  });
});

describe("执行记录", () => {
  it("取消失败后也重读列表", async () => {
    const execution = {
      execution_id: "x1",
      rule_name: "规则",
      event_name: "event.collector.instrument.delist_scheduled",
      status: "EXECUTION_STATUS_RUNNING",
      action_type: "ACTION_TYPE_SYSTEM",
      cleanup_pending: false
    };
    mocks.listEventExecutions.mockResolvedValue({ executions: [execution], page_result: { total: 1 } });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.cancelEventExecution.mockRejectedValue(new Error("取消失败"));
    wrapper = mount(EventExecutions, { global: { plugins: [ArcoVue] } });
    await flushPromises();
    const calls = mocks.listEventExecutions.mock.calls.length;
    await (wrapper.vm as unknown as { act(kind: string, item: unknown): Promise<void> }).act("cancel", execution);
    await flushPromises();
    expect(mocks.listEventExecutions.mock.calls.length).toBe(calls + 1);
  });
});

describe("执行详情", () => {
  it("换记录时清空旧详情，迟到的旧响应被丢弃", async () => {
    let resolveOld!: (value: unknown) => void;
    mocks.getEventExecution.mockImplementationOnce(() => new Promise(done => (resolveOld = done)));
    mocks.getEventExecution.mockResolvedValueOnce({
      execution: {
        execution_id: "new",
        rule_name: "新记录",
        status: "EXECUTION_STATUS_SUCCEEDED",
        action_type: "ACTION_TYPE_NOTIFY_URL"
      },
      payload_json: "{}",
      attempts: [],
      delist_steps: []
    });
    wrapper = mount(ExecutionDrawer, {
      props: { visible: false, executionId: "old", events, refreshToken: 0 },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    await wrapper.setProps({ visible: true });
    await flushPromises();
    await wrapper.setProps({ executionId: "new" });
    await flushPromises();
    resolveOld({
      execution: {
        execution_id: "old",
        rule_name: "旧记录",
        status: "EXECUTION_STATUS_FAILED",
        action_type: "ACTION_TYPE_NOTIFY_URL"
      },
      payload_json: "{}",
      attempts: [],
      delist_steps: []
    });
    await flushPromises();
    expect(document.body.textContent).toContain("新记录");
    expect(document.body.textContent).not.toContain("旧记录");
  });
});
