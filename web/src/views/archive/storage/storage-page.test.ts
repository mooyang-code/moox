import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const GiB = 1024 ** 3;
const api = vi.hoisted(() => ({
  getArchiveOverview: vi.fn(),
  listDeployments: vi.fn(),
  listAllDeploymentHosts: vi.fn()
}));
vi.mock("@/api/archive", () => ({ getArchiveOverview: api.getArchiveOverview }));
vi.mock("@/api/admin/sysdeploy", () => ({
  listDeployments: api.listDeployments,
  listAllDeploymentHosts: api.listAllDeploymentHosts
}));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => ({ selectedSpaceId: "crypto" }) }));
import StoragePage from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const overview = (over: Record<string, unknown> = {}) => ({
  worker: {
    worker_id: "archive-worker@factor",
    active: true,
    root_dir: "/data/moox/archive",
    version: "v0.6.0",
    disk_total_bytes: String(100 * GiB),
    disk_used_bytes: String(30 * GiB),
    disk_free_bytes: String(65 * GiB),
    min_free_bytes: String(5 * GiB),
    staging_bytes: String(GiB),
    last_heartbeat_at: "2026-11-08T00:00:00Z",
    object_store: { endpoint: "127.0.0.1:11473", bucket: "moox-archive", prefix: "moox-archive/" },
    ...over
  }
});

let wrapper: VueWrapper;
beforeEach(() => {
  api.getArchiveOverview.mockReset().mockResolvedValue(overview());
  api.listDeployments
    .mockReset()
    .mockResolvedValue({ deployments: [{ host_id: "factor", component_id: "archive-worker", status: "enabled" }] });
  api.listAllDeploymentHosts
    .mockReset()
    .mockResolvedValue([{ host_id: "factor", label: "因子机", address: "192.168.0.103", status: "enabled" }]);
});
afterEach(() => wrapper?.unmount());

async function mountPage() {
  wrapper = mount(StoragePage, { global: { plugins: [ArcoVue] } });
  await flushPromises();
  return wrapper.text();
}

describe("归档存储页", () => {
  it("执行器所在主机、根目录、磁盘、内置对象存储", async () => {
    const text = await mountPage();
    expect(api.listDeployments).toHaveBeenCalledWith({
      component_id: "archive-worker",
      status: "enabled",
      page: { page: 1, size: 100 }
    });
    expect(text).toContain("因子机（192.168.0.103）");
    expect(text).toContain("/data/moox/archive");
    expect(text).toContain("已用 30 GB / 共 100 GB，可用 65 GB");
    expect(text).not.toContain("磁盘空间不足");
    expect(text).toContain("内置 SeaweedFS");
    expect(text).toContain("不防整盘故障");
    expect(text).toContain("datasets.csv");
  });

  it("可用低于下限时标出磁盘空间不足", async () => {
    api.getArchiveOverview.mockResolvedValue(overview({ disk_used_bytes: String(96 * GiB), disk_free_bytes: String(4 * GiB) }));
    const text = await mountPage();
    expect(text).toContain("磁盘空间不足（可用低于下限 5 GB），已暂停领取新运行");
  });

  it("没配对象存储时说明不能下载", async () => {
    api.getArchiveOverview.mockResolvedValue(overview({ object_store: {} }));
    const text = await mountPage();
    expect(text).toContain("没有配置对象存储");
  });

  it("首次读取失败显示原因和重试", async () => {
    api.getArchiveOverview.mockRejectedValue(new Error("connect ECONNREFUSED"));
    const text = await mountPage();
    expect(text).toContain("无法读取归档服务：connect ECONNREFUSED");
    expect(text).toContain("重试");
  });
});
