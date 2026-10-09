import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { reactive } from "vue";

const api = vi.hoisted(() => ({
  getReplay: vi.fn(),
  listReplays: vi.fn(),
  listReplayBars: vi.fn(),
  startReplay: vi.fn(),
  cancelReplay: vi.fn(),
  listViews: vi.fn(),
  chartSpecs: [] as any[]
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
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => reactive({ selectedSpaceId: "crypto" }) }));
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

function replay(id: string, status: string, metrics = "{}") {
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
    session_id: ""
  };
}

const bars = {
  items: [
    {
      bar_end_time: "2026-09-01T01:00:00Z",
      status: "ok",
      targets: [],
      positions_json: "{}",
      summary_json: "{}",
      bar_return: 0,
      equity: 1,
      turnover: 0,
      fee: 0,
      holdings: 0,
      frozen: 0,
      skip_reason: "",
      unfilled: 0,
      liquidated: 0
    }
  ],
  page: { total: 1 }
};

const stubs = {
  "a-list": {
    props: ["data"],
    template:
      '<div><div v-for="item in data" :key="item.replay_id" class="replay-item" @click="$emit(\'pick\', item)"><slot name="item" :item="item" /></div></div>'
  },
  "a-list-item": { template: '<div class="list-item" @click="$emit(\'click\')"><slot /></div>' },
  "a-popconfirm": { emits: ["ok"], template: '<div class="confirm" @click="$emit(\'ok\')"><slot /></div>' },
  "a-button": { template: "<button><slot /></button>" },
  "a-table": { template: '<div class="table" />' },
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

describe("strategy replay page", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    api.chartSpecs.length = 0;
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
    await wrapper.findAll(".list-item")[1].trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在加载回放详情");
    expect(wrapper.text()).not.toContain("取消回放");
    release(replay("r2", "done"));
    await flushPromises();
    expect(wrapper.text()).not.toContain("正在加载回放详情");
  });

  it("keeps polling the selected running replay when the list page has none", async () => {
    api.listReplays.mockResolvedValueOnce({ items: [replay("r1", "running")], page: { total: 30 } });
    api.listReplays.mockResolvedValue({ items: [replay("r9", "done")], page: { total: 30 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    mountPage();
    await flushPromises();
    const before = api.getReplay.mock.calls.length;
    await vi.advanceTimersByTimeAsync(5000);
    await flushPromises();
    expect(api.getReplay.mock.calls.length).toBeGreaterThan(before);
  });

  it("waits for partial metrics after cancelling", async () => {
    api.listReplays.mockResolvedValue({ items: [replay("r1", "running")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "running"));
    api.cancelReplay.mockResolvedValue({});
    const wrapper = mountPage();
    await flushPromises();
    api.getReplay.mockResolvedValue(replay("r1", "cancelled"));
    api.listReplays.mockResolvedValue({ items: [replay("r1", "cancelled")], page: { total: 1 } });
    await wrapper.find(".confirm").trigger("click");
    await flushPromises();
    expect(wrapper.text()).toContain("正在等待写入截至取消时的部分指标");
    api.getReplay.mockResolvedValue(replay("r1", "cancelled", '{"bars":3,"ok_bars":3,"total_return":0.01,"limitations":[]}'));
    await vi.advanceTimersByTimeAsync(5000);
    await flushPromises();
    expect(wrapper.text()).toContain("截至中断时的部分指标（共 3 根）");
  });

  it("keeps errors from other sources and draws the curve in UTC", async () => {
    api.listViews.mockRejectedValue(new Error("网络错误"));
    api.listReplays.mockResolvedValue({ items: [replay("r1", "done")], page: { total: 1 } });
    api.getReplay.mockResolvedValue(replay("r1", "done", '{"bars":1,"ok_bars":1,"limitations":[]}'));
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.text()).toContain("View 列表加载失败：网络错误");
    const spec = api.chartSpecs[api.chartSpecs.length - 1];
    expect(spec.axes[0].layers[0]).toMatchObject({ timeFormatMode: "utc" });
    expect(spec.tooltip.mark.title.value({ time: Date.parse("2026-09-01T01:00:00Z") })).toBe("2026-09-01 01:00 UTC");
  });
});
