import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { nextTick, reactive } from "vue";

const api = vi.hoisted(() => ({
  getReplay: vi.fn(),
  listReplays: vi.fn(),
  listReplayBars: vi.fn(),
  startReplay: vi.fn(),
  cancelReplay: vi.fn(),
  listViews: vi.fn(),
  chartSpecs: [] as any[],
  space: null as null | { selectedSpaceId: string }
}));
vi.mock("@/api/strategy", () => ({
  getReplay: api.getReplay,
  listReplays: api.listReplays,
  listReplayBars: api.listReplayBars,
  startReplay: api.startReplay,
  cancelReplay: api.cancelReplay
}));
vi.mock("@/api/storage/metadata", () => ({ listViews: api.listViews }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() } }));
vi.mock("vue-router", () => ({ useRoute: () => ({ query: {} }) }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({
  useStrategyStore: () => reactive({ strategies: [{ strategy_id: "s1", name: "动量" }], loadAllStrategies: vi.fn() })
}));
vi.mock("@visactor/vchart", () => ({
  default: class {
    constructor(spec: any) {
      api.chartSpecs.push(spec);
    }
    renderSync = vi.fn();
    updateData = vi.fn();
    release = vi.fn();
  }
}));

import ReplayPage from "./index.vue";

function replay(id: string, status: string, metrics = "{}", extra: Record<string, unknown> = {}) {
  return {
    replay_id: id,
    strategy_id: "s1",
    dsl_yaml: "name: demo",
    space_id: "crypto",
    view_id: "view_a",
    start_time: "2026-09-01T00:00:00Z",
    end_time: "2026-09-02T00:00:00Z",
    fee_bps: 10,
    status,
    progress_time: "",
    metrics_json: metrics,
    error: "",
    created_at: "",
    updated_at: "",
    dsl_hash: "sha256:abc",
    instance_id: "",
    session_id: "",
    ...extra
  };
}

function bar(summary = "{}") {
  return {
    bar_end_time: "2026-09-01T01:00:00Z",
    status: "ok",
    targets: [],
    positions_json: '{"cash":0.5,"positions":{}}',
    summary_json: summary,
    bar_return: 0,
    equity: 1,
    turnover: 0,
    fee: 0,
    holdings: 0,
    frozen: 0,
    skip_reason: "",
    unfilled: 0,
    liquidated: 0
  };
}

const bars = { items: [bar()], page: { total: 1 } };
const noBars = { items: [], page: { total: 0 } };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((ok, fail) => {
    resolve = ok;
    reject = fail;
  });
  return { promise, resolve, reject };
}

const stubs = {
  "a-list": {
    props: ["data"],
    template:
      '<div><div v-for="item in data" :key="item.replay_id" class="replay-item"><slot name="item" :item="item" /></div></div>'
  },
  "a-list-item": { template: '<div class="list-item"><slot /></div>' },
  "a-popconfirm": { emits: ["ok"], template: '<div class="confirm" @click="$emit(\'ok\')"><slot /></div>' },
  "a-button": { template: "<button><slot /></button>" },
  // 表格只渲染展开行，用来检查每期明细。
  "a-table": {
    props: ["data"],
    template:
      '<div class="table"><div v-for="row in data || []" :key="row.bar_end_time || row.id" class="expanded"><slot name="expand-row" :record="row" /></div></div>'
  },
  "a-spin": { props: ["tip"], template: '<div class="spin">{{ tip }}</div>' },
  "a-alert": { template: '<div class="alert"><slot /></div>' }
};

// 其余 Arco 组件只透传默认插槽，避免未注册组件的警告淹没测试输出。
const passthrough = [
  "a-grid",
  "a-grid-item",
  "a-tag",
  "a-empty",
  "a-collapse",
  "a-collapse-item",
  "a-pagination",
  "a-table-column",
  "a-form",
  "a-form-item",
  "a-select",
  "a-option",
  "a-input",
  "a-input-number",
  "a-radio-group",
  "a-radio",
  "a-space",
  "a-textarea",
  "icon-refresh"
];

function mountPage() {
  const all: Record<string, unknown> = { ...stubs };
  for (const name of passthrough) all[name] = { template: "<div><slot /></div>" };
  return mount(ReplayPage, { global: { stubs: all } });
}

/** 推进一个轮询周期（5 秒）并等待请求完成。 */
async function pollOnce() {
  await vi.advanceTimersByTimeAsync(5000);
  await flushPromises();
}

function listText(wrapper: ReturnType<typeof mountPage>) {
  return wrapper
    .findAll("button.replay-row")
    .map(row => row.text())
    .join("\n");
}

describe("strategy replay page", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    api.chartSpecs.length = 0;
    api.space = reactive({ selectedSpaceId: "crypto" });
    for (const fn of [api.getReplay, api.listReplays, api.listReplayBars, api.startReplay, api.cancelReplay, api.listViews])
      fn.mockReset();
    api.listViews.mockResolvedValue({ views: [], page_result: { has_more: false } });
    api.listReplayBars.mockResolvedValue(bars);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("clears the previous replay while a new selection loads", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running"), replay("r2", "done")], page: { total: 2 } });
    let release!: (value: unknown) => void;
    api.getReplay.mockImplementation((id: string) =>
      id === "r1" ? Promise.resolve(replay("r1", "running")) : new Promise(resolve => (release = resolve))
    );
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("取消回放");
    const rows = wrapper.findAll("button.replay-row");
    expect(rows[0].attributes("aria-pressed")).toBe("true");
    await rows[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在加载回放详情");
    expect(wrapper.text()).not.toContain("取消回放");
    release(replay("r2", "done"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在加载回放详情");
    expect(wrapper.findAll("button.replay-row")[1].attributes("aria-pressed")).toBe("true");
  });

  it("keeps polling the selected running replay across cycles when the list page has none", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "running")], page: { total: 30 } });
    api.listReplays.mockResolvedValue({ items: [replay("r9", "done")], page: { total: 30 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    mountPage();
    await flushPromises();
    const initial = api.getReplay.mock.calls.length;
    await pollOnce();
    const afterFirst = api.getReplay.mock.calls.length;
    expect(afterFirst).toBeGreaterThan(initial);
    // 第二个周期列表里已经没有运行中的回放，只因所选回放仍在运行而刷新。
    await pollOnce();
    expect(api.getReplay.mock.calls.length).toBeGreaterThan(afterFirst);
    // 轮询请求不弹全局错误提示。
    expect(api.listReplays.mock.calls.at(-1)?.[1]).toEqual({ silent: true });
    expect(api.getReplay.mock.calls.at(-1)?.[1]).toEqual({ silent: true });
  });

  it("waits for partial metrics only on the cancelled replay", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running"), replay("r2", "done")], page: { total: 2 } });
    api.getReplay.mockImplementation((id: string) =>
      Promise.resolve(id === "r1" ? replay("r1", "running") : replay("r2", "done", '{"bars":2,"ok_bars":2,"limitations":[]}'))
    );
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockImplementation((id: string) =>
      Promise.resolve(id === "r1" ? replay("r1", "cancelled") : replay("r2", "done", '{"bars":2,"ok_bars":2,"limitations":[]}'))
    );
    api.listReplays.mockResolvedValue({ items: [replay("r1", "cancelled"), replay("r2", "done")], page: { total: 2 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入截至取消时的部分指标");
    await wrapper.findAll("button.replay-row")[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入截至取消时的部分指标");
    // 回到被取消的回放，指标写入后显示部分指标。
    api.getReplay.mockImplementation((id: string) =>
      Promise.resolve(
        id === "r1"
          ? replay("r1", "cancelled", '{"bars":3,"ok_bars":3,"total_return":0.01,"limitations":[]}')
          : replay("r2", "done")
      )
    );
    await wrapper.findAll("button.replay-row")[0].trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("截至中断时的部分指标（共 3 根）");
  });

  it("does not wait when the cancelled replay has produced no bars", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "pending")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "pending"));
    api.listReplayBars.mockResolvedValue(noBars);
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(replay("r1", "cancelled"));
    api.listReplays.mockResolvedValue({ items: [replay("r1", "cancelled")], page: { total: 1 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
    const before = api.getReplay.mock.calls.length;
    await pollOnce();
    expect(api.getReplay.mock.calls.length).toBe(before);
  });

  it("stops waiting for partial metrics after the timeout", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(replay("r1", "cancelled"));
    api.listReplays.mockResolvedValue({ items: [replay("r1", "cancelled")], page: { total: 1 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入");
    await vi.advanceTimersByTimeAsync(125_000);
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
    const before = api.getReplay.mock.calls.length;
    await pollOnce();
    expect(api.getReplay.mock.calls.length).toBe(before);
  });

  it("labels partial metrics of a failed replay", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "failed")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "failed", '{"bars":2,"ok_bars":2,"limitations":[]}', { error: "读取超时" }));
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("回放失败：读取超时");
    expect(wrapper.text()).toContain("回放失败，以下是截至中断时的部分指标（共 2 根）");
  });

  it("ignores a stale list response that returns after a newer page", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "running")], page: { total: 30 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    const slow = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => slow.promise);
    await vi.advanceTimersByTimeAsync(5000);
    api.listReplays.mockResolvedValueOnce({
      items: [replay("r21", "done", "{}", { view_id: "view_page2" })],
      page: { total: 30 }
    });
    (wrapper.vm as any).changeReplayPage(2);
    await flushPromises();
    expect(listText(wrapper)).toContain("view_page2");
    slow.resolve({ items: [replay("r1", "running")], page: { total: 30 } });
    await flushPromises();
    expect(listText(wrapper)).toContain("view_page2");
    expect(listText(wrapper)).not.toContain("view_a");
  });

  it("clears list, table and polling errors after a later success", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    // 手动刷新失败，之后的轮询成功：列表错误清除。
    api.listReplays.mockRejectedValueOnce(new Error("断网"));
    await wrapper.find('[aria-label="刷新回放"]').trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("回放列表加载失败：断网");
    await pollOnce();
    expect(wrapper.text()).not.toContain("回放列表加载失败");
    // 轮询连续失败 3 次给出提示，手动刷新成功后清除。
    api.listReplays.mockRejectedValue(new Error("断网"));
    await pollOnce();
    await pollOnce();
    await pollOnce();
    expect(wrapper.text()).toContain("后台暂时无法访问");
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    await wrapper.find('[aria-label="刷新回放"]').trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("后台暂时无法访问");
    // 翻页失败后再翻页成功：周期记录的错误清除。
    api.listReplayBars.mockRejectedValueOnce(new Error("超时"));
    (wrapper.vm as any).changeTablePage(2);
    await flushPromises();
    expect(wrapper.text()).toContain("周期记录加载失败：超时");
    (wrapper.vm as any).changeTablePage(1);
    await flushPromises();
    expect(wrapper.text()).not.toContain("周期记录加载失败");
  });

  it("drops errors of the previous space when the space changes", async () => {
    api.listViews.mockRejectedValueOnce(new Error("网络错误"));
    api.listReplays.mockResolvedValue({ items: [], page: { total: 0 } });
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("View 列表加载失败：网络错误");
    api.space!.selectedSpaceId = "stock";
    await nextTick();
    await flushPromises();
    expect(wrapper.text()).not.toContain("View 列表加载失败");
  });

  it("starts a replay from an unsaved DSL draft", async () => {
    api.listReplays.mockResolvedValue({ items: [], page: { total: 0 } });
    api.startReplay.mockResolvedValue({ replay: replay("r5", "pending"), bar_count: 3, first_bar_end: "", last_bar_end: "" });
    api.getReplay.mockResolvedValue(replay("r5", "pending"));
    const wrapper = mountPage();
    await flushPromises();
    const vm = wrapper.vm as any;
    vm.form.source = "dsl";
    vm.form.dsl_yaml = "name: draft\n";
    vm.form.view_id = "view_a";
    await nextTick();
    const startButton = wrapper.findAll("button").find(button => button.text() === "开始回放");
    await startButton!.trigger("click");
    await flushPromises();
    const request = api.startReplay.mock.calls[0][0];
    expect(request).toMatchObject({ dsl_yaml: "name: draft\n", view_id: "view_a" });
    expect(request.strategy_id).toBeUndefined();
    expect(request.instance_id).toBeUndefined();
  });

  it("shows the bar detail with the buy scale and describes the curve in text", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "done", '{"bars":1,"ok_bars":1,"limitations":[]}'));
    api.listReplayBars.mockResolvedValue({
      items: [bar('{"ledger":{"traded":0.5,"fee":0.001,"buy_scale":0.5}}')],
      page: { total: 1 }
    });
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("现金不足，买入按目标的 50.00% 成交");
    expect(wrapper.find(".chart").attributes("aria-label")).toContain("权益曲线：共 1 根");
    const spec = api.chartSpecs[api.chartSpecs.length - 1];
    expect(spec.axes[0].layers[0]).toMatchObject({ timeFormatMode: "utc" });
    expect(spec.tooltip.mark.title.value({ time: Date.parse("2026-09-01T01:00:00Z") })).toBe("2026-09-01 01:00 UTC");
  });
});
