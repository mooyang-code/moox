import { defineComponent } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { stringify } from "yaml";
import {
  getCatalog,
  getHostRoutes,
  listAllDeploymentHosts,
  listAllPlacements,
  setHostStatus,
  setPlacementStatus
} from "@/api/admin/sysdeploy";
import { monitorApi } from "@/api/monitor";
import { reportControlError } from "@/api/admin/http";
import type { CatalogComponent, CatalogResponse, DeploymentHost, ComponentPlacement } from "@/api/admin/types";
import Deployments from "./index.vue";
vi.mock("@/api/admin/sysdeploy", async original => ({
  ...(await original<typeof import("@/api/admin/sysdeploy")>()),
  getCatalog: vi.fn(),
  getHostRoutes: vi.fn(),
  listAllDeploymentHosts: vi.fn(),
  listAllPlacements: vi.fn(),
  setHostStatus: vi.fn(),
  setPlacementStatus: vi.fn()
}));
vi.mock("@/api/monitor", () => ({ monitorApi: { getOverview: vi.fn() } }));
vi.mock("@/api/admin/http", () => ({ callControl: vi.fn(), reportControlError: vi.fn() }));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn() } }));
const component: CatalogComponent = {
  id: "access",
  name: "外部接入",
  binary: "moox-access",
  scope: "any",
  replicas: "multi",
  protected: false,
  health: { kind: "readyz", port: 11013 },
  services: [{ path: "test.Service", port: 11011, methods: ["Read", "Write"], acl: [{ methods: ["Read"], callers: ["reader"] }] }]
};
const definition = (name = "外部接入"): CatalogResponse => ({
  catalog_yaml: stringify(
    {
      version: 1,
      components: [
        { ...component, name },
        { ...component, id: "host-gateway", name: "主机网关", protected: true }
      ],
      principals: []
    },
    { aliasDuplicateObjects: false }
  ),
  control_host_id: "c-main",
  sha256: "a".repeat(64)
});
const hostRows: DeploymentHost[] = [
  { host_id: "c-main", address: "control.example.test", status: "enabled" },
  { host_id: "storage", address: "storage.example.test", status: "enabled" }
];
const placementItems: ComponentPlacement[] = hostRows.flatMap(host =>
  ["host-gateway", "access"].map(component_id => ({ host_id: host.host_id, component_id, status: "enabled" }))
);
const box = defineComponent({ template: "<div><slot /></div>" });
const input = defineComponent({
  props: ["modelValue"],
  emits: ["update:modelValue"],
  template: '<input :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
});
const select = defineComponent({
  props: ["modelValue"],
  emits: ["update:modelValue"],
  template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>'
});
const option = defineComponent({ props: ["value"], template: '<option :value="value"><slot /></option>' });
const checkbox = defineComponent({
  props: ["modelValue"],
  emits: ["update:modelValue"],
  template:
    '<label><input type="checkbox" :checked="modelValue" @change="$emit(\'update:modelValue\', $event.target.checked)" /><slot /></label>'
});
const drawer = defineComponent({
  props: ["visible"],
  emits: ["update:visible"],
  template:
    '<aside v-if="visible" class="drawer"><button aria-label="关闭详情" @click="$emit(\'update:visible\', false)">关闭</button><slot /></aside>'
});
let wrapper: VueWrapper | undefined;
async function render(query: Record<string, string> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: "/ops/deployments", component: box }] });
  await router.push({ path: "/ops/deployments", query });
  await router.isReady();
  wrapper = mount(Deployments, {
    global: {
      plugins: [router],
      stubs: {
        "a-button": { template: "<button><slot /></button>" },
        "a-alert": box,
        "a-tag": box,
        "a-input": input,
        "a-select": select,
        "a-option": option,
        "a-checkbox": checkbox,
        "a-drawer": drawer
      }
    }
  });
  await flushPromises();
  return { page: wrapper, router };
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  vi.mocked(getCatalog).mockResolvedValue(definition());
  vi.mocked(listAllDeploymentHosts).mockResolvedValue(hostRows);
  vi.mocked(listAllPlacements).mockResolvedValue(placementItems);
  vi.mocked(getHostRoutes).mockImplementation(async host_id => ({
    host_id,
    definition_hash: "definition",
    snapshot_schema_version: 1,
    compiled_at: "2026-10-09T12:00:00Z",
    gateway_status: { expected_hash: "new", applied_hash: "new", last_seen_at: "2026-10-09T12:00:00Z" },
    routes: [
      {
        component_id: "access",
        service_path: "test.Service",
        method: "Read",
        address: "127.0.0.1:11011",
        callers: ["reader"],
        read_only: true
      },
      { component_id: "access", service_path: "test.Service", method: "Write", address: "127.0.0.1:11011", callers: ["writer"] }
    ]
  }));
  vi.mocked(monitorApi.getOverview).mockResolvedValue({
    overview: {
      topology_known: true,
      components: [
        {
          host_id: "c-main",
          component_id: "access",
          status: "healthy",
          instances: [{ instance_id: "process-a", version: "v1", status: "healthy" }]
        },
        { host_id: "storage", component_id: "access", status: "down", reason: "服务不可达" }
      ],
      unregistered: [{ host_id: "other", component_id: "collector", reason: "部署未登记" }]
    }
  });
  vi.mocked(setHostStatus).mockResolvedValue({});
  vi.mocked(setPlacementStatus).mockResolvedValue({});
});
afterEach(() => {
  wrapper?.unmount();
  wrapper = undefined;
  vi.useRealTimers();
});
describe("deployment page", () => {
  it("uses the authoritative control host ID and exposes no protected toggles", async () => {
    const { page } = await render();
    expect(page.find('[aria-label="主机 c-main 启用状态"]').exists()).toBe(false);
    expect(page.find('[aria-label="c-main host-gateway 启用状态"]').exists()).toBe(false);
    expect(page.text()).toContain("受保护");
    expect(page.text()).toContain("process-a · v1");
    expect(page.text()).toContain("moox.toml");
    await page.get('[aria-label="storage access 启用状态"]').trigger("click");
    await flushPromises();
    expect(setPlacementStatus).toHaveBeenCalledWith("storage", "access", "disabled");
    expect(setHostStatus).not.toHaveBeenCalled();
    await page.get('[aria-label="主机 storage 启用状态"]').trigger("click");
    await flushPromises();
    expect(setHostStatus).toHaveBeenCalledWith("storage", "disabled");
  });
  it("selects the exact host and component from Monitor links and keeps a closed drawer closed on polling", async () => {
    const { page } = await render({ tab: "services", host_id: "storage", component_id: "access" });
    expect(page.findAll(".host-group[data-host-id]")).toHaveLength(1);
    expect(page.get(".drawer").text()).toContain("storage.example.test");
    expect(page.get(".drawer").text()).toContain("服务不可达");
    await page.get('[aria-label="关闭详情"]').trigger("click");
    await vi.advanceTimersByTimeAsync(15000);
    await flushPromises();
    expect(page.find(".drawer").exists()).toBe(false);
  });
  it("preserves independent probe and Reporter errors in component details", async () => {
    const probeError = "probe failed\n原始探测错误";
    const reporterError = "reporter missing\n原始上报错误";
    vi.mocked(monitorApi.getOverview).mockResolvedValue({
      overview: {
        topology_known: true,
        components: [
          {
            host_id: "storage",
            component_id: "access",
            status: "down",
            probe: { status: "down", raw_error: probeError },
            reporter: { status: "unknown", raw_error: reporterError }
          }
        ]
      }
    });
    const { page } = await render({ host_id: "storage", component_id: "access" });
    expect(
      page
        .get(".drawer")
        .findAll("pre")
        .map(item => item.text())
    ).toEqual([probeError, reporterError]);
  });
  it("keeps unknown locator targets explicit instead of selecting a different host", async () => {
    const { page } = await render({ host_id: "missing", component_id: "access" });
    expect(page.text()).toContain("未找到主机 missing");
    expect(page.findAll(".host-group[data-host-id]")).toHaveLength(0);
    expect(page.find(".drawer").exists()).toBe(false);
  });
  it("filters services by health without treating enabled deployments as online", async () => {
    const { page } = await render();
    await page.get('[aria-label="部署状态筛选"]').setValue("abnormal");
    expect(page.find('[data-host-id="c-main"] [data-component-id="access"]').exists()).toBe(false);
    expect(page.find('[data-host-id="storage"] [data-component-id="access"]').exists()).toBe(true);
  });
  it("shows per-method callers and combines caller/method filters on the route tab", async () => {
    const { page, router } = await render({ tab: "routes", host_id: "storage" });
    expect(page.text()).toContain("快照格式 v1");
    expect(page.text()).toContain("本次编译");
    expect(page.text()).toContain("2 个方法");
    await page.get('[aria-label="调用方筛选"]').setValue("reader");
    await page.get('[aria-label="方法筛选"]').setValue("Write");
    expect(page.text()).toContain("没有符合筛选条件的路由");
    await page.findAll('[role="tab"]')[0].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.query.tab).toBe("services");
  });
  it("preserves a route read failure and unknown health without inventing an empty route list", async () => {
    vi.mocked(getHostRoutes).mockRejectedValue(new Error("route unavailable"));
    vi.mocked(monitorApi.getOverview).mockRejectedValue(new Error("monitor unavailable"));
    const { page } = await render({ tab: "routes" });
    expect(page.text()).toContain("route unavailable");
    expect(page.text()).toContain("monitor unavailable");
    expect(page.text()).not.toContain("没有符合筛选条件的路由");
  });
  it("reports a failed mutation and keeps the recorded enablement unchanged", async () => {
    vi.mocked(setPlacementStatus).mockRejectedValue(new Error("protected by server"));
    const { page } = await render();
    await page.get('[aria-label="storage access 启用状态"]').trigger("click");
    await flushPromises();
    expect(reportControlError).toHaveBeenCalledWith(expect.objectContaining({ message: "protected by server" }));
    expect(page.get('[aria-label="storage access 启用状态"]').attributes("aria-checked")).toBe("true");
  });
  it("keeps enablement available for a disabled host while disabling its component switches", async () => {
    vi.mocked(listAllDeploymentHosts).mockResolvedValue(
      hostRows.map(host => (host.host_id === "storage" ? { ...host, status: "disabled" } : host))
    );
    const { page } = await render();
    expect(page.find('[data-host-id="storage"]').exists()).toBe(false);
    await page.get('input[type="checkbox"]').setValue(true);
    expect(page.get('[aria-label="storage access 启用状态"]').attributes("disabled")).toBeDefined();
    await page.get('[aria-label="主机 storage 启用状态"]').trigger("click");
    await flushPromises();
    expect(setHostStatus).toHaveBeenCalledWith("storage", "enabled");
  });
  it("marks preserved data unknown and blocks changes when refreshing topology fails", async () => {
    const { page } = await render();
    vi.mocked(getCatalog).mockRejectedValue(new Error("Admin unavailable"));
    await page.get('[aria-label="刷新部署状态"]').trigger("click");
    await flushPromises();
    expect(page.text()).toContain("Admin unavailable");
    expect(page.get('[data-host-id="c-main"] [data-component-id="access"]').text()).toContain("未知");
    expect(page.get('[aria-label="storage access 启用状态"]').attributes("disabled")).toBeDefined();
  });
  it("persists a route host selection through polling", async () => {
    const { page, router } = await render({ tab: "routes", host_id: "storage" });
    await page.get('[aria-label="路由主机"]').setValue("c-main");
    await flushPromises();
    expect(router.currentRoute.value.query.host_id).toBe("c-main");
    await vi.advanceTimersByTimeAsync(15000);
    await flushPromises();
    expect((page.get('[aria-label="路由主机"]').element as HTMLSelectElement).value).toBe("c-main");
  });
  it("stops polling when unmounted", async () => {
    const { page } = await render();
    const calls = vi.mocked(getCatalog).mock.calls.length;
    page.unmount();
    wrapper = undefined;
    await vi.advanceTimersByTimeAsync(30000);
    expect(getCatalog).toHaveBeenCalledTimes(calls);
  });
  it("ignores an older refresh that completes after a newer one", async () => {
    let finish!: (value: CatalogResponse) => void;
    vi.mocked(getCatalog).mockImplementationOnce(
      () =>
        new Promise(resolve => {
          finish = resolve;
        })
    );
    const { page } = await render();
    vi.mocked(getCatalog).mockResolvedValue(definition("新版目录名称"));
    await page.get('[aria-label="刷新部署状态"]').trigger("click");
    await flushPromises();
    expect(page.text()).toContain("新版目录名称");
    finish(definition("旧版目录名称"));
    await flushPromises();
    expect(page.text()).not.toContain("旧版目录名称");
    expect(page.text()).toContain("新版目录名称");
  });
});
