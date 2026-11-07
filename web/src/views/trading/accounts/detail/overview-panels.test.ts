import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h, provide, ref } from "vue";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ summarizeAccountPnl: vi.fn(), listOperatorActions: vi.fn() }));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
import { detailContextKey, type DetailContext } from "./context";
import OperatorActions from "./operator-actions.vue";
import PnlBreakdown from "./pnl-breakdown.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const ok = { ret_info: { code: 0, msg: "" } };
const dataVersion = ref(0);
let wrapper: VueWrapper;

/** 只提供两个面板用到的上下文：当前组合和刷新计数。 */
function mountPanel(panel: object) {
  const logical = ref({ logical_account_id: "l-1" } as LogicalAccount);
  wrapper = mount(
    defineComponent({
      setup() {
        provide(detailContextKey, { logical: computed(() => logical.value), dataVersion } as unknown as DetailContext);
        return () => h(panel as never);
      }
    }),
    { global: { plugins: [ArcoVue] } }
  );
  return wrapper;
}

describe("概览的收益拆解", () => {
  beforeEach(() => {
    dataVersion.value = 0;
    mocks.summarizeAccountPnl.mockReset();
  });
  afterEach(() => wrapper?.unmount());

  it("按资产分别列出，截断时显示警告，并带上组合和时间范围", async () => {
    mocks.summarizeAccountPnl.mockResolvedValue({
      ...ok,
      fees: [{ asset: "USDT", amount: "1.5" }],
      realized_pnl: [{ asset: "USDT", amount: "20" }],
      funding_fees: [{ asset: "USDT", amount: "-0.8" }],
      fill_count: "7",
      funding_available: true,
      funding_truncated: true,
      funding_note: "时间窗超过 90 天"
    });
    mountPanel(PnlBreakdown);
    await flushPromises();
    expect(mocks.summarizeAccountPnl).toHaveBeenCalledWith(
      expect.objectContaining({ logical_account_id: "l-1", start_time: expect.any(String), end_time: expect.any(String) })
    );
    const text = wrapper.text();
    expect(text).toContain("7 笔成交");
    expect(text).toContain("+20.00");
    expect(text).toContain("-0.80");
    expect(text).toContain("资金费只是一部分");
    expect(text).toContain("时间窗超过 90 天");
  });

  it("读取失败显示错误；数据版本变化后重新读取", async () => {
    mocks.summarizeAccountPnl.mockRejectedValueOnce(new Error("网关超时")).mockResolvedValue(ok);
    mountPanel(PnlBreakdown);
    await flushPromises();
    expect(wrapper.text()).toContain("网关超时");
    dataVersion.value += 1;
    await flushPromises();
    expect(mocks.summarizeAccountPnl).toHaveBeenCalledTimes(2);
    expect(wrapper.text()).not.toContain("网关超时");
    expect(wrapper.text()).toContain("0 笔成交");
  });
});

describe("概览的人工操作记录", () => {
  beforeEach(() => {
    dataVersion.value = 0;
    mocks.listOperatorActions.mockReset();
  });
  afterEach(() => wrapper?.unmount());

  it("列出类型、状态、原因和错误，result_json 只提示已解除禁止", async () => {
    mocks.listOperatorActions.mockResolvedValue({
      ...ok,
      actions: [
        {
          action_id: "a-2",
          action_type: "DELIST_FLATTEN",
          reason: "ABC 即将下架",
          status: "COMPLETED",
          result_json: '{"unblocked":true,"accounts":[{"secret":"内部明细"}]}',
          last_error: "",
          created_at: "1700000000000"
        },
        {
          action_id: "a-1",
          action_type: "REDUCE",
          reason: "保证金率过低",
          status: "FAILED",
          result_json: "",
          last_error: "撤单失败",
          created_at: "1699990000000"
        }
      ],
      page_result: { page: 1, size: 10, total: 2 }
    });
    mountPanel(OperatorActions);
    await flushPromises();
    expect(mocks.listOperatorActions).toHaveBeenCalledWith({
      logical_account_id: "l-1",
      action_type: undefined,
      page: { page: 1, size: 10 }
    });
    const text = wrapper.text();
    expect(text).toContain("下架清仓");
    expect(text).toContain("按比例减仓");
    expect(text).toContain("已完成");
    expect(text).toContain("失败");
    expect(text).toContain("标的已解除禁止");
    expect(text).toContain("撤单失败");
    expect(text).not.toContain("内部明细");
  });

  it("空列表和读取失败各有说明", async () => {
    mocks.listOperatorActions.mockResolvedValueOnce(ok).mockRejectedValueOnce(new Error("读取超时"));
    mountPanel(OperatorActions);
    await flushPromises();
    expect(wrapper.text()).toContain("还没有操作记录");
    dataVersion.value += 1;
    await flushPromises();
    expect(wrapper.text()).toContain("读取超时");
    expect(wrapper.text()).not.toContain("还没有操作记录");
  });
});
