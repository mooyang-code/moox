import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { reactive } from "vue";

const ctx = vi.hoisted(() => ({
  push: vi.fn(),
  space: null as any,
  overview: null as any,
  instances: [] as any[],
  targets: {} as Record<string, any>
}));
vi.mock("vue-router", () => ({ useRouter: () => ({ push: ctx.push }) }));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ctx.space }));
vi.mock("@/store/modules/user-info", () => ({ useUserInfoStore: () => ({ account: { user: { userName: "admin123" } } }) }));
vi.mock("@/api/monitor", () => ({ monitorApi: { getOverview: vi.fn(async () => ({ overview: ctx.overview })) } }));
vi.mock("@/api/admin/http", () => ({ callControl: vi.fn(async () => ({ page: { total: 4 } })) }));
vi.mock("@/api/admin/activity", () => ({
  listActivityLogs: vi.fn(async () => ({
    items: [
      {
        id: 1,
        category: "collector.subject_change",
        level: "info",
        title: "dataset_abc 1h：新增 1 个",
        created_at: new Date().toISOString()
      }
    ]
  }))
}));
vi.mock("@/api/cloud-node", () => ({ getNodeList: vi.fn(async () => ({ items: [], total: 46 })) }));
vi.mock("@/api/factor", () => ({
  listFactorSets: vi.fn(async () => ({
    factor_sets: [
      {
        factor_set: { set_id: "f1", space_id: "crypto", source_dataset_id: "dataset_abc", freq: "1m", status: "enabled" },
        members: [{ status: "enabled" }],
        last_run: {
          set_id: "f1",
          last_period_time: String(Math.floor(Date.now() / 1000) - 60),
          last_status: "ok",
          lag_seconds: "8"
        }
      }
    ]
  }))
}));
vi.mock("@/api/storage/metadata", () => ({
  listAllDatasets: vi.fn(async () => [{ dataset_id: "dataset_abc", name: "现货K线", attributes: {} }]),
  listAllViews: vi.fn(async () => []),
  listDataSources: vi.fn(async () => ({ data_sources: [], page_result: { total: 3 } }))
}));
vi.mock("@/api/strategy", () => ({
  listInstances: vi.fn(async () => ({ items: ctx.instances, page: { total: ctx.instances.length } })),
  listStrategies: vi.fn(async () => ({ items: [{ strategy_id: "s1", name: "动量" }], page: { total: 1 } })),
  listStrategyTargets: vi.fn(async (id: string) => ctx.targets[id])
}));
vi.mock("@/api/trade", () => ({ listTradingAccounts: vi.fn(async () => ({ accounts: [], page_result: { total: 0 } })) }));

import Home from "./home.vue";

const iconNames = [
  "icon-check-circle-fill",
  "icon-exclamation-circle-fill",
  "icon-close-circle-fill",
  "icon-minus-circle-fill",
  "icon-compass",
  "icon-experiment",
  "icon-storage",
  "icon-notification",
  "icon-bar-chart",
  "icon-history",
  "icon-safe",
  "icon-left",
  "icon-right"
];
const stubs = {
  "a-button": { template: "<button><slot /></button>" },
  ...Object.fromEntries(iconNames.map(name => [name, { template: `<i class="${name}" />` }]))
};

function instance(id: string, overrides: Record<string, unknown> = {}) {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "",
    ...overrides
  };
}

async function render() {
  const wrapper = mount(Home, { global: { stubs } });
  await flushPromises();
  await flushPromises();
  return wrapper;
}

describe("home page", () => {
  beforeEach(() => {
    ctx.push.mockReset();
    ctx.space = reactive({ selectedSpaceId: "crypto", selectedSpace: { name: "加密货币市场" } });
    ctx.overview = {
      topology_known: true,
      summary: { components: { healthy_count: 22 } },
      alerts: [],
      hosts: [{ host_id: "h1", hostname: "vm-1", status: "healthy", cpu_available: true, cpu_percent: 3 }],
      pipeline: [
        {
          name: "存储",
          datasets: [{ producer: "storage", dataset_id: "dataset_abc", freq: "1m", status: "healthy", lag_seconds: "95" }]
        }
      ]
    };
    ctx.instances = [instance("i1")];
    ctx.targets = {
      i1: {
        targets: [{ instrument_id: "BTC-USDT", target_weight: "0.2" }],
        session_id: "sess",
        bar_end_time: new Date(Date.now() - 60_000).toISOString(),
        valid_until: new Date(Date.now() + 60_000).toISOString(),
        result_id: "r"
      }
    };
  });

  it("states that everything runs normally, with no slogan and an all-clear tile", async () => {
    const wrapper = await render();
    const text = wrapper.text();
    expect(text).toContain("运行正常");
    expect(wrapper.find(".health-card").text()).toContain("22/22 组件");
    expect(text).toContain("加密货币市场");
    expect(wrapper.find(".tile-attention .all-clear").text()).toContain("一切正常");
    expect(wrapper.findAll(".attention-line")).toHaveLength(0);
    expect(text).not.toMatch(/驾驶舱|Quant\.|Think\.|Pattern\./);
  });

  it("puts the quick links in the banner and links them to their pages", async () => {
    const wrapper = await render();
    const links = wrapper.findAll(".banner .quick");
    expect(links.map(link => link.text())).toEqual(["K 线浏览", "因子结果", "策略结果", "策略回放", "交易账户", "监控告警"]);
    await links[0].trigger("click");
    expect(ctx.push).toHaveBeenCalledWith("/collector/tasks?tab=results");
  });

  it("shows four KPI cards that jump to their pages", async () => {
    const wrapper = await render();
    const cards = wrapper.findAll(".kpi");
    expect(cards.map(card => card.find(".kpi-label").text())).toEqual(["策略目标", "因子计算", "数据新鲜度", "当前告警"]);
    expect(cards[0].text()).toContain("1 / 1 有效");
    await cards[3].trigger("click");
    expect(ctx.push).toHaveBeenCalledWith("/ops/monitor");
  });

  it("shows what the strategy holds with weight bars and cash, plus factor progress and activity", async () => {
    const wrapper = await render();
    const text = wrapper.text();
    expect(text).toContain("动量");
    expect(text).toContain("BTC-USDT");
    expect(text).toContain("20.0%");
    expect(text).toContain("现金");
    expect(wrapper.findAll(".weights .meter")).toHaveLength(2);
    expect(text).toContain("现货K线 · 1m");
    expect(text).toContain("现货K线 1h：新增 1 个");
    expect(text).toContain("云节点 46");
  });

  it("lists problems in a fixed-size tile and links each one to where it is handled", async () => {
    ctx.overview.alerts = [
      { id: "a1", severity: "critical", title: "dataset_abc 延迟", object: { type: "dataset", producer: "storage" } }
    ];
    ctx.instances = [instance("i1", { health: "degraded" })];
    const wrapper = await render();
    expect(wrapper.text()).toContain("2 项需要处理");
    const rows = wrapper.findAll(".attention-line");
    expect(rows.map(row => row.find(".ellipsis").text())).toEqual([
      "现货K线 延迟",
      "动量 · i1 · 最近一期被跳过，查看历史结果中的跳过原因"
    ]);
    await rows[1].trigger("click");
    expect(ctx.push).toHaveBeenCalledWith({ name: "strategy-tasks", query: { tab: "results", instance: "i1" } });
  });

  it("folds extra problems into a count instead of growing the tile", async () => {
    ctx.overview.alerts = Array.from({ length: 8 }, (_, i) => ({
      id: `a${i}`,
      severity: "warning",
      title: `告警 ${i}`,
      object: { type: "component", host_id: "h", component_id: "c" }
    }));
    const wrapper = await render();
    expect(wrapper.findAll(".attention-line")).toHaveLength(5);
    expect(wrapper.find(".tile-attention .tile-foot").text()).toContain("另有 3 项");
  });

  it("switches between several strategy instances inside one fixed tile", async () => {
    ctx.instances = [instance("i1"), instance("i2")];
    ctx.targets.i2 = { ...ctx.targets.i1, targets: [{ instrument_id: "ETH-USDT", target_weight: "0.3" }] };
    const wrapper = await render();
    expect(wrapper.find(".tile-strategy").text()).toContain("1 / 2");
    expect(wrapper.findAll(".tile-strategy .strategy-head")).toHaveLength(1);
    await wrapper.find('.tile-strategy [aria-label="下一个实例"]').trigger("click");
    expect(wrapper.find(".tile-strategy").text()).toContain("2 / 2");
  });

  it("asks for a space before anything else", async () => {
    ctx.space.selectedSpaceId = "";
    const wrapper = await render();
    expect(wrapper.text()).toContain("还没有可用的空间");
  });

  it("does not claim everything is normal while data is lagging", async () => {
    ctx.overview.pipeline = [
      {
        name: "存储",
        datasets: [{ producer: "storage", dataset_id: "dataset_abc", freq: "1m", status: "degraded", lag_seconds: "52920" }]
      }
    ];
    const wrapper = await render();
    expect(wrapper.text()).toContain("1 项需要处理");
    expect(wrapper.text()).not.toContain("运行正常");
    expect(wrapper.find(".tile-attention .attention-line").text()).toContain("入库 · 现货K线 · 1m");
    expect(wrapper.find(".tile-attention .attention-line").text()).toContain("14 小时 42 分");
    // 横幅写具体的收盘时间（精确到秒），落后的换成警示色，由人对照当前时间判断。
    const dataFact = wrapper.findAll(".banner-facts span").find(item => item.text().startsWith("数据新鲜度"));
    expect(dataFact?.text()).toMatch(/^数据新鲜度 (\d{2}-\d{2} )?\d{2}:\d{2}:\d{2}$/);
    expect(dataFact?.find("b").classes()).toContain("is-warn");
  });
});
