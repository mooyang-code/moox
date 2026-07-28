import fs from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { METRIC_MONITOR_SERVICE, metricMonitorApi } from "@/api/metric-monitor";

vi.mock("@/api/admin/http", () => ({
  callControl: vi.fn(),
  reportControlError: vi.fn()
}));

const mockedCallControl = vi.mocked(callControl);

describe("observability overview", () => {
  beforeEach(() => mockedCallControl.mockReset());

  it("uses the bounded Monitor overview endpoint across all spaces", async () => {
    mockedCallControl.mockResolvedValue({ overview: { datasets: [] } });
    await metricMonitorApi.getObservabilityOverview();
    expect(mockedCallControl).toHaveBeenCalledWith(METRIC_MONITOR_SERVICE, "GetObservabilityOverview", {});
  });

  it("keeps all five operational sections, filters, tooltips, and explicit empty states", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "observability-overview.vue"), "utf8");
    for (const text of ["服务", "主机", "SCF", "实时 Dataset + Frequency", "Canary / Balance"]) {
      expect(source).toContain(text);
    }
    for (const filter of [
      'v-model="filters.status"',
      'v-model="filters.producer"',
      'v-model="filters.dataset"',
      'v-model="filters.freq"'
    ]) {
      expect(source).toContain(filter);
    }
    expect(source).toContain(':tooltip="true"');
    expect(source).toContain("尚未上报");
    expect(source).toContain("producer stale");
    expect(source).toContain("正常但空结果");
  });

  it("provides a two-column mobile filter layout without fixed table width", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "observability-overview.vue"), "utf8");
    expect(source).toContain("@media (max-width: 640px)");
    expect(source).toContain("grid-template-columns: minmax(0, 1fr) minmax(0, 1fr)");
    expect(source).toContain("overflow-wrap: anywhere");
    expect(source).not.toContain("width: 1440px");
  });
});
