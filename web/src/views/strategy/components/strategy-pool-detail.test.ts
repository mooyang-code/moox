import { mount } from "@vue/test-utils";
import { defineComponent, h } from "vue";
import { describe, expect, it, vi } from "vitest";
import type { StrategyResult } from "@/api/strategy-types";
import { buildPoolPeriods, POOL_PALETTE } from "@/views/strategy/pool-model";
import InstanceResults from "./strategy-instance-results.vue";
import PoolDetail from "./strategy-pool-detail.vue";

vi.mock("@/views/strategy/components/strategy-target-table.vue", () => ({
  default: defineComponent({ setup: () => () => h("div", { class: "target-table-stub" }) })
}));
vi.mock("@/views/strategy/components/strategy-result-table.vue", () => ({
  default: defineComponent({ setup: () => () => h("div", { class: "result-table-stub" }) })
}));

// 选择框做成原生 select，便于在测试里切换期。
const stubs = {
  "a-select": defineComponent({
    props: ["modelValue"],
    emits: ["change"],
    setup(props, { slots, emit }) {
      return () =>
        h(
          "select",
          {
            class: "period-select",
            value: props.modelValue,
            onChange: (event: Event) => emit("change", (event.target as HTMLSelectElement).value)
          },
          slots.default?.()
        );
    }
  }),
  "a-option": defineComponent({
    props: ["value"],
    setup(props, { slots }) {
      return () => h("option", { value: props.value }, slots.default?.());
    }
  }),
  "a-alert": { template: "<div><slot /></div>" }
};

function member(id: string, extra: Record<string, unknown> = {}) {
  return { id, status: "ok", equity: 1.02, base: "0.5", ratio_before: "0.5", ratio: "0.5", gross: "0.4", net: "0.4", ...extra };
}

function poolResult(
  id: string,
  time: string,
  members: unknown[],
  extra: Partial<StrategyResult> = {},
  pool: Record<string, unknown> = {}
): StrategyResult {
  return {
    result_id: id,
    instance_id: "i",
    session_id: "s",
    bar_end_time: time,
    valid_until: "",
    status: "ok",
    skip_reason: "",
    dsl_hash: "",
    targets: [],
    summary_json: JSON.stringify({ gross: "0.8", pool: { method: "rotation", members, ...pool } }),
    input_json: "{}",
    rule_states_json: "{}",
    publish_status: "none",
    created_at: "",
    next_params_json: "",
    params_delivered_at: "",
    params_error: "",
    ...extra
  } as StrategyResult;
}

const results = [
  poolResult(
    "r2",
    "2026-11-01T02:00:00Z",
    [
      member("trend_a", { equity: 1.05, rank: 1.5, timing: "1", targets: { "BTC-USDT|spot": "0.2", "ETH-USDT|swap": "-0.1" } }),
      member("trend_b", {
        status: "skipped",
        reason: "factor_missing",
        detail: "缺少 bias",
        equity: 0.8,
        bankrupt: true,
        ratio: "0",
        timing: "0"
      })
    ],
    {},
    { timing: true }
  ),
  poolResult(
    "r1",
    "2026-11-01T01:00:00Z",
    [member("trend_a", { equity: 1.01, timing: "1" }), member("trend_b", { equity: 0.95, timing: "1" })],
    { status: "skipped", skip_reason: "factor_missing" },
    { timing: true }
  )
];

function mountDetail(items = results) {
  return mount(PoolDetail, { props: { periods: buildPoolPeriods(items) }, global: { stubs } });
}

describe("策略池详情", () => {
  it("渲染成员图例、净值与比例图、说明和最新一期的明细表", () => {
    const wrapper = mountDetail();
    expect(wrapper.text()).toContain("成员净值是按收盘价成交的模拟账本曲线");
    expect(wrapper.text()).toContain("只覆盖当前已加载的结果页");
    const legend = wrapper.findAll(".legend-item");
    expect(legend.map(item => item.find(".mono").text())).toEqual(["trend_a", "trend_b"]);
    // 爆仓的成员在图例里标注，另一个没有
    expect(legend[0].find(".bankrupt").exists()).toBe(false);
    expect(legend[1].find(".bankrupt").text()).toBe("爆仓");
    // 图例色块与折线同色，按成员顺序固定
    expect(legend[0].find(".swatch").attributes("style")).toContain(POOL_PALETTE[0]);
    const lines = wrapper.findAll("svg[aria-label='成员净值曲线'] .line");
    expect(lines).toHaveLength(2);
    expect(lines[0].attributes("style")).toContain(POOL_PALETTE[0]);
    expect(lines[1].attributes("style")).toContain(POOL_PALETTE[1]);
    expect(wrapper.findAll("svg[aria-label='每期分配比例堆叠面积图'] .area")).toHaveLength(2);

    // 默认是最新一期：trend_b 本期没通过且爆仓
    const rows = wrapper.findAll(".member-row");
    expect(rows).toHaveLength(2);
    expect(rows[0].text()).toContain("trend_a");
    expect(rows[0].text()).toContain("1.0500");
    expect(rows[0].text()).toContain("1.5");
    expect(rows[0].text()).toContain("持有");
    expect(rows[1].text()).toContain("没通过");
    expect(rows[1].text()).toContain("因子状态缺失（factor_missing）");
    expect(rows[1].text()).toContain("爆仓");
    expect(rows[1].text()).toContain("空仓");
  });

  it("择时没有开启时不显示择时色带与择时列", () => {
    const wrapper = mountDetail(
      results.map(item => ({ ...item, summary_json: item.summary_json.replace('"timing":true', '"timing":false') }))
    );
    expect(wrapper.find("svg[aria-label^='成员 trend_a 的择时状态']").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("择时状态");
    expect(wrapper.findAll("th").map(item => item.text())).not.toContain("择时");
  });

  it("开启择时时每个成员一行色带，持有与空仓分开着色", () => {
    const wrapper = mountDetail();
    const strips = wrapper.findAll(".timing-strip");
    expect(strips).toHaveLength(2);
    expect(strips[0].findAll("rect.hold")).toHaveLength(2);
    expect(strips[1].findAll("rect.hold")).toHaveLength(1);
    expect(strips[1].findAll("rect.flat")).toHaveLength(1);
  });

  it("切换期后明细随之变化，展开成员行可看到标的与市场两列的贡献权重", async () => {
    const wrapper = mountDetail();
    const options = wrapper.findAll("option");
    expect(options.map(item => item.attributes("value"))).toEqual(["r2", "r1"]);
    expect(options[1].text()).toContain("本期跳过");

    await wrapper.find(".toggle").trigger("click");
    const targets = wrapper.find(".targets-table");
    expect(targets.findAll("th").map(item => item.text())).toEqual(["标的", "市场", "贡献权重", "方向"]);
    const cells = targets.findAll(":scope > tbody > tr").map(row => row.findAll("td").map(cell => cell.text()));
    expect(cells).toEqual([
      ["BTC-USDT", "现货", "20.00%", "多"],
      ["ETH-USDT", "永续", "-10.00%", "空"]
    ]);

    await wrapper.find("select.period-select").setValue("r1");
    expect(wrapper.findAll(".member-row")[0].text()).toContain("1.0100");
    expect(wrapper.findAll(".member-row")[1].text()).toContain("正常");
    // 没有综合名次分的期不显示该列
    expect(wrapper.findAll("th").map(item => item.text())).not.toContain("综合名次分");
  });

  it("手动选的期不在已加载结果里时回到最新一期", async () => {
    const wrapper = mountDetail();
    await wrapper.find("select.period-select").setValue("r1");
    await wrapper.setProps({ periods: buildPoolPeriods([results[0]]) });
    expect(wrapper.findAll(".member-row")[0].text()).toContain("1.0500");
  });

  it("只有一期时也能画出曲线和面积", () => {
    const wrapper = mountDetail([results[0]]);
    expect(wrapper.findAll("svg[aria-label='成员净值曲线'] .line")).toHaveLength(2);
    expect(wrapper.findAll(".area")).toHaveLength(2);
    expect(wrapper.html()).not.toContain("NaN");
  });

  it("20 个以上成员时色板循环", () => {
    const members = Array.from({ length: 22 }, (_, index) => member(`m${index}`));
    const wrapper = mountDetail([poolResult("r1", "2026-11-01T01:00:00Z", members)]);
    const swatches = wrapper.findAll(".legend-item .swatch");
    expect(swatches).toHaveLength(22);
    expect(swatches[POOL_PALETTE.length].attributes("style")).toContain(POOL_PALETTE[0]);
  });
});

describe("策略池详情里的漂移与组合约束", () => {
  const driftPool = {
    drift: {
      phase: "drift",
      sides: [{ side: "long", rebalance: "periodic", trigger: "periodic", moved: true }],
      freeze: { change: 0.02, frozen: false }
    },
    adjustments: [
      { kind: "gross", side: "all", factor: 0.8, long_before: 1, short_before: 0.5, long_after: 0.8, short_after: 0.4 }
    ],
    switch: { index: 0, empty: "probe", from: "trend_b", to: ["trend_a"], moved: "0.1" }
  };
  const members = [
    member("trend_a", { base: "0.6", ratio_before: "0.6", drifted: "0.62", ratio: "0.6", composed: "0.7" }),
    member("trend_b", { base: "0.4", ratio_before: "0.4", drifted: "0.38", ratio: "0.4", composed: "0.3" })
  ];
  const driftResults = [
    poolResult("d1", "2026-11-01T01:00:00Z", [], {
      summary_json: JSON.stringify({
        pool: { method: "drift", members, ...driftPool },
        limits: [{ instrument_id: "BTC-USDT", side: "long", excess: "0.05", round: 1, to: "trend_a" }]
      })
    })
  ];

  it("drift 的列名按分配系数写，并给出漂移后系数、合成比例与当期分配说明", () => {
    const wrapper = mount(PoolDetail, {
      props: { periods: buildPoolPeriods(driftResults), source: "周期表当前页" },
      global: { stubs }
    });
    expect(wrapper.text()).toContain("漂移分配");
    expect(wrapper.text()).toContain("每期分配系数");
    expect(wrapper.text()).toContain("只覆盖当前已加载的周期表当前页");
    const headers = wrapper.findAll(".pool-table > thead th").map(item => item.text());
    expect(headers).toEqual(expect.arrayContaining(["目标比例", "漂移前系数", "漂移后系数", "分配系数", "合成比例"]));
    expect(headers).not.toContain("步长前比例");
    const row = wrapper.findAll(".member-row")[0].text();
    expect(row).toContain("62.00%");
    expect(row).toContain("70.00%");
    const notes = wrapper.find(".pool-notes").text();
    expect(notes).toContain("照常漂移");
    expect(notes).toContain("定期再平衡，本根触发（定期），已移动侧内构成");
    expect(notes).toContain("窗口涨跌 2.00%");
    expect(notes).toContain("整体 ×0.8");
    expect(notes).toContain("probe 为空：trend_b 的 10.00% 转给 trend_a");
    expect(notes).toContain("BTC-USDT 多头超出 5.00%（第 1 轮）→ trend_a");
  });

  it("fixed、rotation 没写约束时不出漂移列、合成列与分配说明", () => {
    const wrapper = mountDetail();
    const headers = wrapper.findAll(".pool-table > thead th").map(item => item.text());
    expect(headers).toEqual(expect.arrayContaining(["基础比例", "步长前比例", "实际比例"]));
    expect(headers).not.toContain("漂移后系数");
    expect(headers).not.toContain("合成比例");
    expect(wrapper.find(".pool-notes").exists()).toBe(false);
  });
});

describe("实例结果面板里的策略池", () => {
  const props = {
    instance: { instance_id: "i", enabled: true, session_id: "s", health: "ok" },
    snapshot: null,
    targetState: "valid",
    total: 2,
    page: 1,
    pageSize: 20,
    scope: "session",
    loading: false,
    error: ""
  };

  it("结果里带 pool 时在当前目标与历史结果之间显示策略池卡片", () => {
    const wrapper = mount(InstanceResults, { props: { ...props, results } as never, global: { stubs } });
    const order = wrapper
      .findAll(".target-table-stub, section[aria-label='策略池'], .result-table-stub")
      .map(item => item.classes()[0] ?? item.attributes("aria-label"));
    expect(order).toEqual(["target-table-stub", "pool", "result-table-stub"]);
  });

  it("没有 pool 的实例不渲染策略池卡片", () => {
    const plain = [poolResult("r1", "2026-11-01T01:00:00Z", [], {}, {})].map(item => ({
      ...item,
      summary_json: '{"gross":"0.8"}'
    }));
    const wrapper = mount(InstanceResults, { props: { ...props, results: plain } as never, global: { stubs } });
    expect(wrapper.find("section[aria-label='策略池']").exists()).toBe(false);
    expect(wrapper.find(".target-table-stub").exists()).toBe(true);
    expect(wrapper.find(".result-table-stub").exists()).toBe(true);
  });
});
