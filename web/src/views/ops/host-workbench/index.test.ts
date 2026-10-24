import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listAllDeploymentHosts: vi.fn(),
  getCurrentMetrics: vi.fn(),
  createSSHSession: vi.fn(),
  disconnectSSHSession: vi.fn(),
  query: {} as Record<string, unknown>,
  replace: vi.fn()
}));

vi.mock("vue-router", () => ({
  useRoute: () => ({ query: mocks.query }),
  useRouter: () => ({ replace: mocks.replace })
}));
vi.mock("@/api/admin/sysdeploy", () => ({ listAllDeploymentHosts: mocks.listAllDeploymentHosts }));
vi.mock("@/api/modules/host-monitor", () => ({ getCurrentMetrics: mocks.getCurrentMetrics }));
vi.mock("@/api/modules/ssh", () => ({
  createSSHSession: mocks.createSSHSession,
  disconnectSSHSession: mocks.disconnectSSHSession
}));

vi.mock("@/components/page-title-tabs/index.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/views/container/ssh-hosts/ssh-hosts.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/views/container/ssh-file-manager/ssh-file-manager.vue", () => ({ default: { template: "<div />" } }));
vi.mock("./host-monitor.vue", () => ({ default: { template: "<div />" } }));
vi.mock("@/views/container/ssh-terminal/ssh-terminal.vue", () => ({
  default: { props: { initialHostId: String }, template: '<div class="terminal-stub">{{ initialHostId }}</div>' }
}));

import HostWorkbench from "./index.vue";

const hosts = [
  { host_id: "control", label: "控制机", address: "203.0.113.10", status: "enabled", ssh_port: 22, ssh_user: "ubuntu" },
  { host_id: "factor", label: "因子机", address: "192.0.2.30", status: "enabled", ssh_port: 0, reachability: "outbound" }
];

// 终端在 a-modal 里，会被传送到 body。
const terminal = () => document.body.querySelector(".terminal-stub");

function mountWorkbench() {
  return mount(HostWorkbench, { global: { plugins: [ArcoVue] } });
}

describe("host workbench deep links", () => {
  beforeEach(() => {
    mocks.query = {};
    mocks.listAllDeploymentHosts.mockReset().mockResolvedValue(hosts);
    mocks.getCurrentMetrics.mockReset().mockResolvedValue({ metrics: [], storage_available: true, data_gap: false });
  });

  it("opens the terminal for a host_id link on the hosts tab", async () => {
    mocks.query = { host_id: "control" };
    const wrapper = mountWorkbench();
    await flushPromises();
    expect(terminal()?.textContent).toBe("control");
    wrapper.unmount();
  });

  it("does not open a terminal when a monitor link only locates the host", async () => {
    mocks.query = { tab: "monitor", host_id: "control" };
    const wrapper = mountWorkbench();
    await flushPromises();
    expect(terminal()).toBeNull();
    wrapper.unmount();
  });

  it("does not open a terminal for a host without registered SSH", async () => {
    mocks.query = { host_id: "factor" };
    const wrapper = mountWorkbench();
    await flushPromises();
    expect(terminal()).toBeNull();
    wrapper.unmount();
  });
});
