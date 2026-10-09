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
  loadAllStrategies: vi.fn(),
  reportControlError: vi.fn(),
  // serverNow 是请求层按 Date 头换算的服务端时间；null 表示还没有读到。
  serverNow: null as number | null,
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
vi.mock("@/api/admin/http", () => ({ reportControlError: api.reportControlError, serverNow: () => api.serverNow }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn() } }));
vi.mock("vue-router", () => ({ useRoute: () => ({ query: {} }) }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => api.space }));
vi.mock("@/store/modules/strategy", () => ({
  useStrategyStore: () =>
    reactive({ strategies: [{ strategy_id: "s1", name: "动量" }], loadAllStrategies: api.loadAllStrategies })
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

import { Message } from "@arco-design/web-vue";
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

/** 刚被取消的回放：updated_at 是当前时间（假时钟）。 */
function justCancelled(id: string, metrics = "{}") {
  return replay(id, "cancelled", metrics, { updated_at: new Date().toISOString() });
}
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
  // 表格只渲染展开行，用来检查每期明细；data-loading 反映加载遮罩。
  "a-table": {
    props: ["data", "loading"],
    template:
      '<div class="table" :data-loading="loading ? \'true\' : \'false\'"><div v-for="row in data || []" :key="row.bar_end_time || row.id" class="expanded"><slot name="expand-row" :record="row" /></div></div>'
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
    for (const fn of [
      api.getReplay,
      api.listReplays,
      api.listReplayBars,
      api.startReplay,
      api.cancelReplay,
      api.listViews,
      api.loadAllStrategies,
      api.reportControlError
    ])
      fn.mockReset();
    vi.mocked(Message.error).mockClear();
    api.serverNow = null;
    api.loadAllStrategies.mockResolvedValue(undefined);
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
    expect(rows[0].attributes("aria-current")).toBe("true");
    expect(rows[1].attributes("aria-current")).toBeUndefined();
    await rows[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在加载回放详情");
    expect(wrapper.text()).not.toContain("取消回放");
    release(replay("r2", "done"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在加载回放详情");
    expect(wrapper.findAll("button.replay-row")[1].attributes("aria-current")).toBe("true");
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
      Promise.resolve(id === "r1" ? justCancelled("r1") : replay("r2", "done", '{"bars":2,"ok_bars":2,"limitations":[]}'))
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
        id === "r1" ? justCancelled("r1", '{"bars":3,"ok_bars":3,"total_return":0.01,"limitations":[]}') : replay("r2", "done")
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
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    api.listReplays.mockResolvedValue({ items: [justCancelled("r1")], page: { total: 1 } });
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
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    api.listReplays.mockResolvedValue({ items: [justCancelled("r1")], page: { total: 1 } });
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

  it("scopes the waiting hint to the replay being waited on", async () => {
    const stale = replay("r2", "cancelled", "{}", { updated_at: "2026-01-01T00:00:00Z" });
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running"), stale], page: { total: 2 } });
    api.getReplay.mockImplementation((id: string) => Promise.resolve(id === "r1" ? replay("r1", "running") : stale));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockImplementation((id: string) => Promise.resolve(id === "r1" ? justCancelled("r1") : stale));
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入");
    // r2 早就取消、没有指标：r1 还在等待，r2 不应显示等待提示。
    await wrapper.findAll("button.replay-row")[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
  });

  it("does not wait when a running replay is cancelled before producing bars", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.listReplayBars.mockResolvedValue(noBars);
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    api.listReplays.mockResolvedValue({ items: [justCancelled("r1")], page: { total: 1 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
  });

  it("starts waiting when a replay is observed cancelled elsewhere", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    // 在别的标签页或 CLI 取消：轮询看到 cancelled、已有周期、还没有指标，开始等待并继续轮询。
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    await pollOnce();
    expect(wrapper.text()).toContain("正在等待写入");
    const before = api.getReplay.mock.calls.length;
    await pollOnce();
    expect(api.getReplay.mock.calls.length).toBeGreaterThan(before);
  });

  it("discards an older list response even for the same page", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    const slow = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => slow.promise);
    await wrapper.find('[aria-label="刷新回放"]').trigger("click");
    api.listReplays.mockResolvedValueOnce({
      items: [replay("r1", "running", "{}", { view_id: "view_newer" })],
      page: { total: 1 }
    });
    await pollOnce();
    expect(listText(wrapper)).toContain("view_newer");
    // 手动加载仍在进行：加载态只跟踪手动加载，轮询应用了更新的结果也不结束它。
    expect(wrapper.find('[aria-label="刷新回放"]').attributes("loading")).toBe("true");
    slow.resolve({ items: [replay("r1", "running", "{}", { view_id: "view_older" })], page: { total: 1 } });
    await flushPromises();
    expect(listText(wrapper)).toContain("view_newer");
    expect(wrapper.find('[aria-label="刷新回放"]').attributes("loading")).not.toBe("true");
  });

  it("keeps the loading state until the latest manual load finishes", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "done")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "done"));
    const wrapper = mountPage();
    await flushPromises();
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise);
    const refresh = () => wrapper.find('[aria-label="刷新回放"]');
    await refresh().trigger("click");
    await refresh().trigger("click");
    first.resolve({ items: [replay("r1", "done")], page: { total: 1 } });
    await flushPromises();
    // 较早的一次手动加载结束时，较晚的一次还在进行：加载态保持。
    expect(refresh().attributes("loading")).toBe("true");
    second.resolve({ items: [replay("r1", "done")], page: { total: 1 } });
    await flushPromises();
    expect(refresh().attributes("loading")).not.toBe("true");
  });

  it("ignores errors of superseded list and table requests", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    const slowList = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => slowList.promise);
    await wrapper.find('[aria-label="刷新回放"]').trigger("click");
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    await pollOnce();
    slowList.reject(new Error("旧请求失败"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("回放列表加载失败");
    const slowTable = deferred<unknown>();
    api.listReplayBars.mockImplementationOnce(() => slowTable.promise);
    (wrapper.vm as any).changeTablePage(2);
    api.listReplayBars.mockResolvedValue(bars);
    (wrapper.vm as any).changeTablePage(1);
    await flushPromises();
    slowTable.reject(new Error("旧页失败"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("周期记录加载失败");
  });

  it("clears the polling warning after a successful selection", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running"), replay("r2", "done")], page: { total: 2 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    api.listReplays.mockRejectedValue(new Error("断网"));
    await pollOnce();
    await pollOnce();
    await pollOnce();
    expect(wrapper.text()).toContain("后台暂时无法访问");
    api.getReplay.mockResolvedValue(replay("r2", "done"));
    await wrapper.findAll("button.replay-row")[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("后台暂时无法访问");
  });

  it("silences every polling request and keeps the table steady", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    mountPage();
    await flushPromises();
    api.listReplayBars.mockClear();
    await pollOnce();
    const calls = api.listReplayBars.mock.calls;
    expect(calls.length).toBeGreaterThanOrEqual(2);
    for (const call of calls) expect(call[2]).toEqual({ silent: true });
    expect(calls.some(call => call[1]?.brief)).toBe(true);
    expect(calls.some(call => !call[1]?.brief)).toBe(true);
  });

  it("drops the previous space state when the space changes", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockReset();
    const wrapper = mountPage();
    await flushPromises();
    const pendingPoll = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => pendingPoll.promise);
    await vi.advanceTimersByTimeAsync(5000);
    const pendingCancel = deferred<unknown>();
    api.cancelReplay.mockImplementationOnce(() => pendingCancel.promise);
    await wrapper.find(".confirm").trigger("click");
    const nextList = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => nextList.promise);
    api.space!.selectedSpaceId = "stock";
    await nextTick();
    // 新空间的列表返回之前不显示旧空间的回放。
    expect(listText(wrapper)).toBe("");
    // 旧空间的轮询失败不计数，取消返回也不再动新空间的状态。
    pendingPoll.reject(new Error("断网"));
    pendingCancel.resolve({});
    await flushPromises();
    expect((wrapper.vm as any).pollFailures).toBe(0);
    expect((wrapper.vm as any).awaitingMetrics).toBeNull();
    nextList.resolve({ items: [replay("s1", "done", "{}", { view_id: "view_stock" })], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("s1", "done", "{}", { view_id: "view_stock" }));
    await flushPromises();
    expect(listText(wrapper)).toContain("view_stock");
    expect(api.getReplay.mock.calls.at(-1)?.[0]).toBe("s1");
  });

  it("waits after its own cancel even when the browser clock is far ahead", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    // 浏览器比服务端快 10 分钟：取消时间在浏览器看来是 10 分钟前，但这是本页刚取消的。
    const cancelledAt = new Date(Date.now() - 600_000).toISOString();
    api.getReplay.mockResolvedValue(replay("r1", "cancelled", "{}", { updated_at: cancelledAt }));
    api.listReplays.mockResolvedValue({ items: [replay("r1", "cancelled")], page: { total: 1 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入");
  });

  it("waits when polling observes the transition to cancelled regardless of clocks", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(
      replay("r1", "cancelled", "{}", { updated_at: new Date(Date.now() - 600_000).toISOString() })
    );
    await pollOnce();
    expect(wrapper.text()).toContain("正在等待写入");
  });

  it("judges a directly opened cancelled replay by server time", async () => {
    const cancelledAt = Date.now() - 600_000;
    const cancelled = replay("r1", "cancelled", "{}", { updated_at: new Date(cancelledAt).toISOString() });
    api.listReplays.mockResolvedValue({ items: [cancelled], page: { total: 1 } });
    api.getReplay.mockResolvedValue(cancelled);
    // 服务端看来 30 秒前刚取消（浏览器时钟快 10 分钟）：等待。
    api.serverNow = cancelledAt + 30_000;
    let wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入");
    wrapper.unmount();
    // 服务端看来 10 分钟前取消：不等待。
    api.serverNow = cancelledAt + 600_000;
    wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
    wrapper.unmount();
    // 读不到服务端时间：不等待。
    api.serverNow = null;
    wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在等待写入");
  });

  it("keeps the user's selection made while a started replay is being listed", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done"), replay("r2", "done")], page: { total: 2 } });
    api.getReplay.mockImplementation((id: string) => Promise.resolve(replay(id, "done")));
    api.startReplay.mockResolvedValue({ replay: replay("r9", "pending"), bar_count: 3, first_bar_end: "", last_bar_end: "" });
    const wrapper = mountPage();
    await flushPromises();
    const vm = wrapper.vm as any;
    vm.form.strategy_id = "s1";
    vm.form.view_id = "view_a";
    await nextTick();
    const list = deferred<unknown>();
    api.listReplays.mockImplementationOnce(() => list.promise);
    await wrapper
      .findAll("button")
      .find(button => button.text() === "开始回放")!
      .trigger("click");
    await flushPromises();
    await wrapper.findAll("button.replay-row")[1].trigger("click");
    list.resolve({ items: [replay("r9", "pending"), replay("r1", "done"), replay("r2", "done")], page: { total: 3 } });
    await flushPromises();
    expect(vm.selectedId).toBe("r2");
  });

  it("keeps the table page when reloading after a cancel", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    (wrapper.vm as any).changeTablePage(3);
    await flushPromises();
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    api.listReplayBars.mockClear();
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    const tableCalls = api.listReplayBars.mock.calls.filter(call => !call[1]?.brief);
    expect(tableCalls.at(-1)?.[1]).toMatchObject({ page: 3 });
    expect((wrapper.vm as any).tablePage).toBe(3);
  });

  it("reports a failed cancel once and not after a space switch", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    const failure = new Error("网络错误");
    api.cancelReplay.mockRejectedValueOnce(failure);
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(api.reportControlError).toHaveBeenCalledTimes(1);
    expect(api.reportControlError).toHaveBeenCalledWith(failure);
    expect(Message.error).not.toHaveBeenCalled();
    const pending = deferred<unknown>();
    api.cancelReplay.mockImplementationOnce(() => pending.promise);
    await wrapper.find(".confirm").trigger("click");
    api.listReplays.mockResolvedValue({ items: [], page: { total: 0 } });
    api.space!.selectedSpaceId = "stock";
    await nextTick();
    pending.reject(new Error("旧空间的取消失败"));
    await flushPromises();
    expect(api.reportControlError).toHaveBeenCalledTimes(1);
  });

  it("drops in-flight requests after unmount", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done")], page: { total: 1 } });
    const pending = deferred<unknown>();
    api.getReplay.mockImplementation(() => pending.promise);
    const wrapper = mountPage();
    await flushPromises();
    wrapper.unmount();
    api.listReplayBars.mockClear();
    pending.resolve(replay("r1", "done"));
    await flushPromises();
    expect(api.listReplayBars).not.toHaveBeenCalled();
    expect(api.chartSpecs.length).toBe(0);
  });

  it("clears the detail error when a new selection starts", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done"), replay("r2", "done")], page: { total: 2 } });
    api.getReplay.mockRejectedValueOnce(new Error("读取失败"));
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("回放详情加载失败：读取失败");
    const slow = deferred<unknown>();
    api.getReplay.mockImplementationOnce(() => slow.promise);
    await wrapper.findAll("button.replay-row")[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).not.toContain("回放详情加载失败");
    slow.resolve(replay("r2", "done"));
    await flushPromises();
  });

  it("keeps the definition list error across space switches", async () => {
    api.loadAllStrategies.mockRejectedValueOnce(new Error("定义服务不可用"));
    api.listReplays.mockResolvedValue({ items: [], page: { total: 0 } });
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("策略定义加载失败：定义服务不可用");
    api.space!.selectedSpaceId = "stock";
    await nextTick();
    await flushPromises();
    expect(wrapper.text()).toContain("策略定义加载失败：定义服务不可用");
  });

  it("does not show the table mask while polling", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.find(".table").attributes("data-loading")).toBe("false");
    const slow = deferred<unknown>();
    api.listReplayBars.mockImplementation((_id: string, params: any) => (params?.brief ? Promise.resolve(bars) : slow.promise));
    await pollOnce();
    expect(api.listReplayBars.mock.calls.some(call => !call[1]?.brief)).toBe(true);
    expect(wrapper.find(".table").attributes("data-loading")).toBe("false");
    slow.resolve(bars);
    await flushPromises();
  });

  it("ends the list loading state before the automatic selection finishes", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done")], page: { total: 1 } });
    const slow = deferred<unknown>();
    api.getReplay.mockImplementation(() => slow.promise);
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("正在加载回放详情");
    expect(wrapper.find('[aria-label="刷新回放"]').attributes("loading")).not.toBe("true");
    slow.resolve(replay("r1", "done"));
    await flushPromises();
  });

  it("stops waiting for partial metrics when the space changes", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(justCancelled("r1"));
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect((wrapper.vm as any).awaitingMetrics).not.toBeNull();
    api.listReplays.mockResolvedValue({ items: [], page: { total: 0 } });
    api.space!.selectedSpaceId = "stock";
    await nextTick();
    await flushPromises();
    expect((wrapper.vm as any).awaitingMetrics).toBeNull();
  });

  it("keeps the table mask until the latest manual table load finishes", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "done"));
    const wrapper = mountPage();
    await flushPromises();
    const first = deferred<unknown>();
    const second = deferred<unknown>();
    api.listReplayBars.mockImplementationOnce(() => first.promise).mockImplementationOnce(() => second.promise);
    (wrapper.vm as any).changeTablePage(2);
    (wrapper.vm as any).changeTablePage(3);
    await nextTick();
    first.resolve(bars);
    await flushPromises();
    expect(wrapper.find(".table").attributes("data-loading")).toBe("true");
    second.resolve(bars);
    await flushPromises();
    expect(wrapper.find(".table").attributes("data-loading")).toBe("false");
  });
});
