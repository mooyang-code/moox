import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, type Component } from "vue";
import { createMemoryHistory, createRouter, type Router } from "vue-router";
import { createPinia, setActivePinia } from "pinia";
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
  cancelEventExecution: vi.fn(),
  retryEventExecution: vi.fn()
}));
vi.mock("@/api/callback-dispatcher", async () => ({
  ...(await vi.importActual<typeof import("@/api/callback-dispatcher")>("@/api/callback-dispatcher")),
  ...mocks
}));
vi.mock("@/api/admin/http", () => ({ reportControlError: vi.fn(), callControl: vi.fn() }));

import RuleDialog from "./subscriptions/rule-dialog.vue";
import EventRules from "./subscriptions/index.vue";
import ExecutionDrawer from "./executions/execution-drawer.vue";
import EventExecutions from "./executions/index.vue";
import EventCatalog from "./catalog/index.vue";

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
    scope_fields: [{ field: "exchange", title: "交易所", options: ["binance", "okx"] }],
    sample_json: "",
    payload_fields: []
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
  setActivePinia(createPinia());
});

const Blank = defineComponent({ render: () => null });
let router: Router;

/** 带路由挂载整页：页面用 URL 保存筛选、链接到别的页。 */
async function mountPage(page: Component, url: string, attach = false) {
  router = createRouter({
    history: createMemoryHistory(),
    routes: ["/events", "/trading/accounts/:id"].map(path => ({
      path,
      component: Blank
    }))
  });
  await router.push(url);
  wrapper = mount(page, { global: { plugins: [ArcoVue, router] }, ...(attach ? { attachTo: document.body } : {}) });
  await flushPromises();
  return wrapper;
}

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
    await mountPage(EventRules, "/events?tab=subscriptions", true);
    (wrapper!.vm as unknown as { requestToggle(rule: EventRule, enabled: boolean): void }).requestToggle(system, false);
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

  it("最近一次执行链接到按这条订阅筛选的执行记录", async () => {
    mocks.listEventRules.mockResolvedValue({
      rules: [rule({ last_execution: { status: "EXECUTION_STATUS_FAILED", at_ms: 1 } })],
      page_result: { total: 1 }
    });
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    await mountPage(EventRules, "/events?tab=subscriptions");
    expect(wrapper!.findAll("a").map(link => link.attributes("href"))).toContain("#/events?tab=executions&rule_id=r1");
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
    await mountPage(EventRules, "/events?tab=subscriptions");
    const vm = wrapper!.vm as unknown as {
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
    expect((wrapper!.vm as unknown as { rows: EventRule[] }).rows).toHaveLength(10);
  });

  it("迟到的旧页响应被丢弃", async () => {
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    let resolveOld!: (value: unknown) => void;
    mocks.listEventRules.mockImplementationOnce(() => new Promise(done => (resolveOld = done)));
    mocks.listEventRules.mockResolvedValue({ rules: [rule({ rule_id: "fresh" })], page_result: { total: 1 } });
    await mountPage(EventRules, "/events?tab=subscriptions");
    void (wrapper!.vm as unknown as { load(): Promise<void> }).load();
    await flushPromises();
    resolveOld({ rules: [rule({ rule_id: "stale" })], page_result: { total: 1 } });
    await flushPromises();
    expect((wrapper!.vm as unknown as { rows: EventRule[] }).rows.map(item => item.rule_id)).toEqual(["fresh"]);
  });
});

describe("事件页签", () => {
  it("按事件数订阅条数（从全部订阅里数）；下架事件的详情里写系统订阅的状态", async () => {
    const system = rule({ rule_id: "sys", name: "卖出下架币", system: true, enabled: false, action_type: "ACTION_TYPE_SYSTEM" });
    mocks.listAllEventRules.mockResolvedValue([rule({ rule_id: "x" }), system]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    await mountPage(EventCatalog, "/events", true);
    expect(wrapper!.text()).toContain("2 条");
    (wrapper!.vm as unknown as { infoEvent: SubscribableEvent | null }).infoEvent = events[0];
    await flushPromises();
    expect(document.body.textContent).toContain("系统订阅「卖出下架币」：已停用");
    expect(document.body.textContent).toContain("下架前 24 小时平掉策略持有的部分");
  });

  it("点“订阅”切到订阅页签并带上预选的事件；订阅页签据此打开新建并预选", async () => {
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.listEventRules.mockResolvedValue({ rules: [], page_result: { total: 0 } });
    await mountPage(EventCatalog, "/events");
    (wrapper!.vm as unknown as { subscribe(event: SubscribableEvent): void }).subscribe(events[0]);
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "subscriptions", new: events[0].event_name });
    wrapper!.unmount();
    wrapper = mount(EventRules, { global: { plugins: [ArcoVue, router] } });
    await flushPromises();
    const vm = wrapper.vm as unknown as { ruleVisible: boolean; presetEvent: string };
    expect(vm.ruleVisible).toBe(true);
    expect(vm.presetEvent).toBe(events[0].event_name);
    expect(router.currentRoute.value.query).toEqual({ tab: "subscriptions" });
  });

  it("系统订阅读取失败时写“读取失败”，登记弹窗也不当成已启用", async () => {
    mocks.listAllEventRules.mockRejectedValue(new Error("读不到"));
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: ["SYSTEM_ACTION_SELL_DELISTED_HOLDINGS"] });
    await mountPage(EventCatalog, "/events", true);
    (wrapper!.vm as unknown as { infoEvent: SubscribableEvent | null }).infoEvent = events[0];
    await flushPromises();
    expect(document.body.textContent).toContain("系统订阅：读取失败");
    expect(document.body.textContent).not.toContain("：已启用");
    (wrapper!.vm as unknown as { infoEvent: SubscribableEvent | null }).infoEvent = null;
    [...document.querySelectorAll<HTMLButtonElement>("button")].find(item => item.textContent?.trim() === "手工登记")!.click();
    await flushPromises();
    expect(document.body.textContent).toContain("无法确认系统订阅");
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
    await mountPage(EventExecutions, "/events?tab=executions");
    const calls = mocks.listEventExecutions.mock.calls.length;
    await (wrapper!.vm as unknown as { act(kind: string, item: unknown): Promise<void> }).act("cancel", execution);
    await flushPromises();
    expect(mocks.listEventExecutions.mock.calls.length).toBe(calls + 1);
  });

  it("系统动作的重新执行先弹窗说明后果，确认后才执行；通知类直接执行", async () => {
    const failed = (patch: Record<string, unknown>) => ({
      execution_id: "x2",
      rule_name: "规则",
      event_name: "event.collector.instrument.delist_scheduled",
      subject_id: "binance:SPOT:FOOUSDT",
      status: "EXECUTION_STATUS_FAILED",
      action_type: "ACTION_TYPE_SYSTEM",
      cleanup_pending: false,
      ...patch
    });
    mocks.listEventExecutions.mockResolvedValue({ executions: [], page_result: { total: 0 } });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.retryEventExecution.mockResolvedValue({});
    await mountPage(EventExecutions, "/events?tab=executions", true);
    const vm = wrapper!.vm as unknown as { requestAct(kind: string, item: unknown): void };

    vm.requestAct("retry", failed({}));
    await flushPromises();
    expect(mocks.retryEventExecution).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("会再次对持有 binance:SPOT:FOOUSDT 的组合禁止买入");
    expect(document.body.textContent).toContain("不会重做平仓");
    const confirm = [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(
      item => item.textContent?.trim() === "重新执行"
    );
    confirm?.click();
    await flushPromises();
    expect(mocks.retryEventExecution).toHaveBeenCalledWith("x2");

    mocks.retryEventExecution.mockClear();
    vm.requestAct("retry", failed({ execution_id: "x3", action_type: "ACTION_TYPE_NOTIFY_URL" }));
    await flushPromises();
    expect(mocks.retryEventExecution).toHaveBeenCalledWith("x3");
  });
});

describe("执行记录的 URL", () => {
  it("带 status=failed 打开时按失败筛选；打开记录、换筛选都写回 URL", async () => {
    mocks.listEventExecutions.mockResolvedValue({ executions: [], page_result: { total: 0 } });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.getEventExecution.mockReturnValue(new Promise(() => undefined));
    await mountPage(EventExecutions, "/events?tab=executions&status=failed&rule_id=r1");
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(1);
    expect(mocks.listEventExecutions.mock.calls[0][0]).toMatchObject({ rule_id: "r1", status: "EXECUTION_STATUS_FAILED" });

    const vm = wrapper!.vm as unknown as {
      openDetail(item: { execution_id: string }): void;
      onDrawerVisible(visible: boolean): void;
      filters: { status: string };
      onFilterChange(): void;
    };
    vm.openDetail({ execution_id: "x9" });
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "executions", rule_id: "r1", status: "failed", execution: "x9" });
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(1); // 自己写回 URL 不重读列表

    vm.onDrawerVisible(false);
    vm.filters.status = "";
    vm.onFilterChange();
    await flushPromises();
    expect(router.currentRoute.value.query).toEqual({ tab: "executions", rule_id: "r1" });
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(2);
  });

  it("带 execution= 打开（刷新浏览器）时打开详情并读取；详情开着时自动刷新暂停", async () => {
    vi.useFakeTimers();
    mocks.listEventExecutions.mockResolvedValue({ executions: [], page_result: { total: 0 } });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    mocks.getEventExecution.mockReturnValue(new Promise(() => undefined));
    await mountPage(EventExecutions, "/events?tab=executions&execution=x1");
    expect(mocks.getEventExecution).toHaveBeenCalledWith("x1");
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(1);

    (wrapper!.vm as unknown as { onDrawerVisible(visible: boolean): void }).onDrawerVisible(false);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(2);
    expect(mocks.listEventExecutions.mock.lastCall?.[1]).toEqual({ silentError: true });
    vi.useRealTimers();
  });

  it("从别处带着新的筛选跳进来（页面复用）时按新的 URL 重读", async () => {
    mocks.listEventExecutions.mockResolvedValue({ executions: [], page_result: { total: 0 } });
    mocks.listAllEventRules.mockResolvedValue([]);
    mocks.listSubscribableEvents.mockResolvedValue({ events, availableSystemActions: [] });
    await mountPage(EventExecutions, "/events?tab=executions");
    await router.push("/events?tab=executions&status=failed");
    await flushPromises();
    expect(mocks.listEventExecutions).toHaveBeenCalledTimes(2);
    expect(mocks.listEventExecutions.mock.calls[1][0]).toMatchObject({ status: "EXECUTION_STATUS_FAILED" });
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
