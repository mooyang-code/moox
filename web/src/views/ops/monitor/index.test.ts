import { defineComponent } from "vue";
import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { monitorApi, type HealthOverview } from "@/api/monitor";
import { listAllDatasets } from "@/api/storage/metadata";
import HealthMonitor from "./index.vue";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));
vi.mock("@/api/storage/metadata", () => ({
  listAllDatasets: vi.fn(async () => [{ dataset_id: "dataset_abc", name: "现货分钟K线" }]),
  listAllViews: vi.fn(async () => [])
}));
const mockedCallControl = vi.mocked(callControl);
const box = defineComponent({ template: "<div><slot /></div>" });
const hidden = defineComponent({ props: ["visible"], template: '<div v-if="visible"><slot /></div>' });
let wrapper: VueWrapper | undefined;
function render(overview: HealthOverview) {
  mockedCallControl.mockResolvedValue({ overview });
  wrapper = mount(HealthMonitor, {
    global: {
      stubs: {
        "a-space": box,
        "a-button": { template: '<button class="refresh"><slot /></button>' },
        "a-drawer": hidden,
        "a-modal": hidden,
        "a-form": box,
        "a-form-item": box,
        "a-select": box,
        "a-option": box,
        "a-input": box,
        "a-checkbox": box,
        "icon-arrow-right": true,
        "icon-check-circle-fill": true,
        "icon-exclamation-circle-fill": true,
        "icon-close-circle-fill": true,
        "icon-minus-circle-fill": true
      }
    }
  });
  return wrapper;
}
const generated = "2026-10-16T03:00:00Z";
const healthy: HealthOverview = {
  generated_at: generated,
  topology_known: true,
  components: [{ host_id: "control", component_id: "admin", name: "管理后台", status: "healthy" }],
  hosts: [{ host_id: "control", agent_id: "pwU2", status: "healthy", cpu_available: true, cpu_percent: 3, cpu_threshold: 80 }],
  incident_window_hours: 48
};
const refreshButton = (page: VueWrapper) => page.findAll("button.refresh")[0];

describe("monitor page", () => {
  beforeEach(() => {
    mockedCallControl.mockReset();
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
  });

  it("states the conclusion first and only turns green with a known topology", async () => {
    const page = render({ ...healthy, topology_known: false });
    await flushPromises();
    expect(page.find(".verdict").text()).toContain("部署清单尚未同步");
    expect(page.find(".verdict").classes()).not.toContain("is-ok");
    mockedCallControl.mockResolvedValue({ overview: healthy });
    await refreshButton(page).trigger("click");
    await flushPromises();
    expect(page.find(".verdict").text()).toContain("全部正常");
    expect(page.find(".verdict").classes()).toContain("is-ok");
    expect(page.find('[aria-label="当前告警"]').exists()).toBe(false);
    expect(page.text()).toContain("最近 48 小时没有告警");
  });

  it("lists alerts grouped by trigger time and expands their evidence", async () => {
    const page = render({
      ...healthy,
      alerts: [
        {
          id: "fetch",
          severity: "critical",
          title: "行情采集 scf_timer 协调",
          reason: "采集计划指标已停止上报",
          raw_error: "metrics stale",
          triggered_at: "2026-10-16T02:58:20Z",
          object: { type: "business", kind: "market_fetch" }
        },
        {
          id: "reporter",
          severity: "warning",
          title: "行情采集服务（control 节点）· 运行指标上报",
          triggered_at: "2026-10-16T02:58:00Z",
          object: { type: "component", host_id: "control", component_id: "collector" }
        }
      ]
    });
    await flushPromises();
    expect(page.find(".verdict").text()).toContain("2 项告警进行中");
    const group = page.find(".alert-group");
    expect(group.text()).toContain("2 项同一时间触发，可能是同一个原因");
    expect(group.findAll(".alert-line")).toHaveLength(2);
    expect(group.find(".locator").attributes("href")).toBe("#/collector/tasks?tab=results");
    await group.find(".alert-line__main").trigger("click");
    expect(group.find(".raw-error pre").text()).toBe("metrics stale");
  });

  it("folds flaps in the 48-hour timeline", async () => {
    const page = render({
      ...healthy,
      incidents: [
        {
          id: "flap",
          severity: "warning",
          title: "运行指标上报",
          triggered_at: "2026-10-16T02:30:00Z",
          resolved_at: "2026-10-16T02:30:29Z"
        },
        {
          id: "long",
          severity: "critical",
          title: "策略结果",
          triggered_at: "2026-10-15T20:00:00Z",
          resolved_at: "2026-10-15T23:09:00Z",
          reminder_count: 37
        }
      ]
    });
    await flushPromises();
    const timeline = page.find(".timeline");
    expect(timeline.findAll(".incident")).toHaveLength(1);
    expect(timeline.text()).toContain("3 小时 9 分后恢复");
    expect(timeline.text()).toContain("提醒 37 次");
    await timeline.find(".link-button").trigger("click");
    expect(timeline.findAll(".incident")).toHaveLength(2);
    expect(timeline.text()).toContain("29 秒后恢复 · 闪断");
  });

  it("shows only the latest incidents until expanded", async () => {
    const page = render({
      ...healthy,
      incidents: Array.from({ length: 12 }, (_, index) => ({
        id: `i${index}`,
        severity: "critical",
        title: `告警 ${index}`,
        triggered_at: new Date(Date.parse(generated) - (index + 1) * 3_600_000).toISOString(),
        resolved_at: new Date(Date.parse(generated) - (index + 1) * 3_600_000 + 600_000).toISOString()
      }))
    });
    await flushPromises();
    const timeline = page.find(".timeline");
    expect(timeline.findAll(".incident")).toHaveLength(8);
    expect(timeline.find(".link-button").text()).toBe("展开其余 4 条");
    await timeline.find(".link-button").trigger("click");
    expect(timeline.findAll(".incident")).toHaveLength(12);
  });

  it("opens component details from the host dots and keeps raw errors untranslated", async () => {
    const raw = "x509: certificate expired\n原始错误";
    const page = render({
      ...healthy,
      components: [
        {
          host_id: "control",
          component_id: "web-host",
          name: "控制台前端",
          status: "down",
          reason: "健康检查失败",
          probe: { status: "down", reason: "证书已过期", raw_error: raw }
        },
        { host_id: "storage", component_id: "web-host", name: "控制台前端", status: "healthy" }
      ],
      unregistered: [{ host_id: "control", component_id: "rogue", name: "rogue", reason: "进程在上报，但没有部署记录" }]
    });
    await flushPromises();
    expect(page.findAll(".host-row")).toHaveLength(2);
    expect(page.find(".issues").text()).toContain("控制台前端（control）");
    expect(page.find(".issues").text()).toContain("未登记进程 rogue（control）");
    await page.find(".component-chip").trigger("click");
    expect(page.find(".detail").text()).toContain("control · web-host");
    expect(page.find(".detail .raw-error pre").text()).toBe(raw);
  });

  it("shows dataset names and only the datasets that need attention", async () => {
    const page = render({
      ...healthy,
      pipeline: [
        {
          id: "storage",
          status: "healthy",
          datasets: [
            {
              producer: "storage",
              space_id: "crypto",
              dataset_id: "dataset_abc",
              freq: "1m",
              status: "degraded",
              reason: "最近成功结果已过期",
              lag_seconds: "600",
              lag_limit_seconds: "180"
            },
            {
              producer: "storage",
              space_id: "crypto",
              dataset_id: "dataset_def",
              freq: "1h",
              status: "healthy",
              output_watermark_at: "2026-10-16T02:00:00Z",
              lag_limit_seconds: "7200"
            }
          ]
        }
      ]
    });
    await flushPromises();
    await flushPromises();
    const rows = page.findAll(".dataset-table tbody tr");
    expect(rows).toHaveLength(1);
    expect(rows[0].text()).toContain("现货分钟K线");
    expect(rows[0].text()).toContain("最近成功结果已过期");
    expect(rows[0].find(".lag").classes()).toContain("is-bad");
    expect(vi.mocked(listAllDatasets)).toHaveBeenCalledWith("crypto", { spaceId: "crypto" });
    await page.findAll(".layer--wide .link-button")[0].trigger("click");
    expect(page.findAll(".dataset-table tbody tr")).toHaveLength(2);
  });

  it("keeps the last data on errors and ignores a late older refresh", async () => {
    const page = render(healthy);
    await flushPromises();
    let finishOld!: (value: unknown) => void;
    mockedCallControl.mockImplementationOnce(
      () =>
        new Promise(resolve => {
          finishOld = resolve;
        })
    );
    await refreshButton(page).trigger("click");
    mockedCallControl.mockResolvedValueOnce({ overview: { ...healthy, hosts: [{ host_id: "control", status: "unknown" }] } });
    await refreshButton(page).trigger("click");
    await flushPromises();
    finishOld({ overview: healthy });
    await flushPromises();
    expect(page.find(".verdict").text()).toContain("1 项需要关注");
    mockedCallControl.mockRejectedValueOnce(new Error("加载失败"));
    await refreshButton(page).trigger("click");
    await flushPromises();
    expect(page.find(".verdict").text()).toContain("监控数据刷新失败");
    expect(page.find(".verdict").text()).toContain("加载失败");
    expect(page.find(".verdict").classes()).not.toContain("is-ok");
    expect(page.findAll(".host-card")).toHaveLength(1);
  });

  it("uses the structured overview and global notification endpoints", async () => {
    mockedCallControl.mockResolvedValue({ overview: healthy });
    await monitorApi.getOverview({ space_id: "crypto" });
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "GetHealthOverview", { space_id: "crypto" });
    mockedCallControl.mockResolvedValue({ channel: { configured: false } });
    await monitorApi.getNotification();
    expect(mockedCallControl).toHaveBeenLastCalledWith("monitor", "GetNotificationChannel", {});
  });
});
