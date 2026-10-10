import { defineComponent } from "vue";
import { mount, flushPromises, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { callControl } from "@/api/admin/http";
import { monitorApi, type HealthOverview } from "@/api/monitor";
import HealthMonitor from "./index.vue";

vi.mock("@/api/admin/http", () => ({ callControl: vi.fn() }));
const mockedCallControl = vi.mocked(callControl);
const box = defineComponent({ template: '<div><slot name="title" /><slot /></div>' });
const hidden = defineComponent({ props: ["visible"], template: '<div v-if="visible"><slot /></div>' });
let wrapper: VueWrapper | undefined;
function render(overview: HealthOverview) {
  mockedCallControl.mockResolvedValue({ overview });
  wrapper = mount(HealthMonitor, {
    global: {
      stubs: {
        "a-space": box,
        "a-button": { template: "<button><slot /></button>" },
        "a-card": box,
        "a-tag": box,
        "a-alert": box,
        "a-drawer": hidden,
        "a-modal": hidden,
        "a-form": box,
        "a-form-item": box,
        "a-select": box,
        "a-option": box,
        "a-input": box,
        "a-checkbox": box
      }
    }
  });
  return wrapper;
}
const healthy: HealthOverview = {
  topology_known: true,
  summary: { alert_count: 0, attention_count: 0, unknown_count: 0, healthy_count: 1 },
  components: []
};
describe("health overview v2", () => {
  beforeEach(() => {
    mockedCallControl.mockReset();
    vi.useFakeTimers();
  });
  afterEach(() => {
    wrapper?.unmount();
    wrapper = undefined;
    vi.useRealTimers();
  });
  it("uses authoritative summary and requires a known deployment topology before showing green", async () => {
    const page = render({ ...healthy, topology_known: false });
    await flushPromises();
    expect(page.text()).toContain("部署清单尚未完成同步");
    expect(page.text()).not.toContain("系统一切正常");
    mockedCallControl.mockResolvedValue({ overview: healthy });
    await page.findAll("button")[0].trigger("click");
    await flushPromises();
    expect(page.text()).toContain("系统一切正常");
  });
  it("keeps same-component hosts separate and exposes raw probe error without translating it", async () => {
    const raw = "x509: certificate expired\n原始错误";
    const page = render({
      ...healthy,
      summary: { attention_count: 1, healthy_count: 1 },
      components: [
        {
          host_id: "control",
          component_id: "web-host",
          name: "目录名称",
          status: "down",
          reason: "服务探测失败",
          probe: { status: "down", reason: "证书已过期", raw_error: raw },
          reporter: { status: "healthy", reason: "监控上报正常" }
        },
        {
          host_id: "storage",
          component_id: "web-host",
          name: "目录名称",
          status: "unchecked",
          probe: { status: "unchecked", reason: "不探测" }
        }
      ]
    });
    await flushPromises();
    expect(page.findAll(".health-card--interactive")).toHaveLength(2);
    expect(page.text()).toContain("control · web-host");
    expect(page.text()).toContain("storage · web-host");
    expect(page.text()).toContain("不探测");
    expect(page.text()).not.toContain("系统一切正常");
    await page.find(".health-card--interactive").trigger("click");
    expect(page.find(".raw-error pre").text()).toBe(raw);
    expect(page.find(".raw-error summary").text()).toBe("原始错误");
  });
  it("shows host alerts, unregistered processes and independent console page failure", async () => {
    const page = render({
      ...healthy,
      summary: { alert_count: 1, attention_count: 2, unregistered_count: 1 },
      alerts: [
        {
          id: "host-alert",
          severity: "critical",
          title: "主机 control · CPU 使用率",
          reason: "CPU 使用率超过阈值",
          object: { type: "host", host_id: "control", agent_id: "PHYSICAL01" },
          raw_error: "sample detail"
        }
      ],
      unregistered: [{ host_id: "control", component_id: "rogue", name: "rogue", reason: "进程在上报，但没有部署记录" }],
      business_checks: [
        {
          check_id: "console-page:control:console-proxy",
          kind: "console-page",
          status: "down",
          reason: "控制台页面请求失败",
          raw_error: "HTTP 502"
        }
      ]
    });
    await flushPromises();
    expect(page.text()).toContain("主机 control · CPU 使用率");
    expect(page.text()).toContain("未登记进程（1）");
    expect(page.text()).toContain("控制台页面请求失败");
    expect(page.text()).not.toContain("系统一切正常");
    await page.find(".health-card--interactive").trigger("click");
    expect(page.text()).toContain("PHYSICAL01");
    expect(page.find(".locator").attributes("href")).toBe("#/ops/hosts?tab=monitor&agent_id=PHYSICAL01&host_id=control");
    expect(page.findAll(".raw-error pre").some(node => node.text() === "sample detail")).toBe(true);
  });
  it("retains last data on errors and prevents a late refresh from overwriting newer facts", async () => {
    const page = render(healthy);
    await flushPromises();
    let finishOld!: (value: unknown) => void;
    mockedCallControl.mockImplementationOnce(
      () =>
        new Promise(resolve => {
          finishOld = resolve;
        })
    );
    await page.findAll("button")[0].trigger("click");
    mockedCallControl.mockResolvedValueOnce({ overview: { ...healthy, summary: { unknown_count: 1 } } });
    await page.findAll("button")[0].trigger("click");
    await flushPromises();
    finishOld({ overview: healthy });
    await flushPromises();
    expect(page.text()).not.toContain("系统一切正常");
    mockedCallControl.mockRejectedValueOnce(new Error("加载失败"));
    await page.findAll("button")[0].trigger("click");
    await flushPromises();
    expect(page.text()).toContain("加载失败");
    expect(page.text()).toContain("保留上次成功加载的数据");
    expect(page.find(".clear-state").exists()).toBe(false);
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
