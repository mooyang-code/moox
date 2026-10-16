import { flushPromises, mount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import type { HealthOverview } from "@/api/monitor";
import ArrivalChart from "./arrival-chart.vue";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));
const charts = vi.hoisted(() => ({ created: 0, released: 0 }));
vi.mock("@visactor/vchart", () => ({
  default: class {
    constructor() {
      charts.created++;
    }
    renderSync() {
      return this;
    }
    release() {
      charts.released++;
    }
  }
}));
const mockedCallControl = vi.mocked(callControl);

const names = { datasets: { dataset_spot_1m: "现货分钟K线" }, views: {} };
const nowMs = Date.parse("2026-10-16T19:00:00Z");
const overview: HealthOverview = {
  pipeline: [
    {
      id: "storage",
      datasets: [
        { producer: "storage", space_id: "crypto", dataset_id: "dataset_spot_1m", freq: "1m", timezone: "UTC" },
        { producer: "storage", space_id: "stockcn", dataset_id: "dataset_stockcn_1m", freq: "1m", timezone: "Asia/Shanghai" }
      ]
    }
  ]
};

describe("arrival chart", () => {
  beforeEach(() => {
    mockedCallControl.mockReset();
    charts.created = 0;
  });

  it("queries today in the market zone and switches zone with the chain", async () => {
    mockedCallControl.mockResolvedValue({
      timezone: "UTC",
      day: "2026-10-16",
      start_at: "2026-10-16T00:00:00Z",
      end_at: "2026-10-17T00:00:00Z",
      series: [
        {
          stage: "storage",
          dataset_id: "dataset_spot_1m",
          lateness_limit_seconds: "30",
          points: [{ close_at: "2026-10-16T18:55:00Z", delay_seconds: "1" }]
        }
      ]
    });
    const page = mount(ArrivalChart, { props: { overview, names, nowMs } });
    await flushPromises();
    expect(mockedCallControl).toHaveBeenCalledWith("monitor", "QueryPipelineArrivalHistory", {
      space_id: "crypto",
      dataset_id: "dataset_spot_1m",
      freq: "1m",
      day: "2026-10-16"
    });
    expect(page.text()).toContain("日期（UTC）");
    expect(page.find(".arrival-summary").text()).toContain("入库：现货分钟K线");
    expect(charts.created).toBe(1);

    // A 股按北京时间：UTC 19:00 已是北京时间 10-17。
    await page.find("select").setValue("stockcn/dataset_stockcn_1m/1m");
    await flushPromises();
    expect(page.text()).toContain("日期（UTC+8）");
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "QueryPipelineArrivalHistory", {
      space_id: "stockcn",
      dataset_id: "dataset_stockcn_1m",
      freq: "1m",
      day: "2026-10-17"
    });
    page.unmount();
  });

  it("reloads today's curves on page refresh but not past days", async () => {
    mockedCallControl.mockResolvedValue({ timezone: "UTC", day: "2026-10-16", series: [] });
    const page = mount(ArrivalChart, { props: { overview, names, nowMs } });
    await flushPromises();
    expect(mockedCallControl).toHaveBeenCalledTimes(1);
    await page.setProps({ nowMs: nowMs + 30_000 });
    await flushPromises();
    expect(mockedCallControl).toHaveBeenCalledTimes(2);
    expect(page.text()).not.toContain("加载中");

    await page.find('input[type="date"]').setValue("2026-10-15");
    await flushPromises();
    expect(mockedCallControl).toHaveBeenCalledTimes(3);
    await page.setProps({ nowMs: nowMs + 60_000 });
    await flushPromises();
    expect(mockedCallControl).toHaveBeenCalledTimes(3);

    // 跨过 UTC 0 点：看历史日期的不跟着跳。
    await page.setProps({ nowMs: Date.parse("2026-10-17T00:00:30Z") });
    await flushPromises();
    expect((page.find('input[type="date"]').element as HTMLInputElement).value).toBe("2026-10-15");
    page.unmount();
  });

  it("follows today across midnight", async () => {
    mockedCallControl.mockResolvedValue({ timezone: "UTC", series: [] });
    const page = mount(ArrivalChart, { props: { overview, names, nowMs: Date.parse("2026-10-16T23:59:50Z") } });
    await flushPromises();
    await page.setProps({ nowMs: Date.parse("2026-10-17T00:00:20Z") });
    await flushPromises();
    expect((page.find('input[type="date"]').element as HTMLInputElement).value).toBe("2026-10-17");
    expect(mockedCallControl).toHaveBeenLastCalledWith(
      "monitor",
      "QueryPipelineArrivalHistory",
      expect.objectContaining({ day: "2026-10-17" })
    );
    page.unmount();
  });

  it("shows the query error instead of an empty chart", async () => {
    mockedCallControl.mockRejectedValue(new Error("到达历史查询参数无效"));
    const page = mount(ArrivalChart, { props: { overview, names, nowMs } });
    await flushPromises();
    expect(page.find(".arrival-error").text()).toBe("到达历史查询参数无效");
    expect(charts.created).toBe(0);
    page.unmount();
  });
});
