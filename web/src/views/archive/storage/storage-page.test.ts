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
    last_heartbeat_at: String(Date.parse("2026-11-08T00:00:00Z")),
    object_store: { endpoint: "cos.ap-nanjing.myqcloud.com", bucket: "moox-archive", prefix: "moox-archive/" },
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
  it("执行器所在主机、根目录、磁盘、外部对象存储", async () => {
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
    expect(text).toContain("外部 S3 兼容存储");
    expect(text).not.toContain("内置 SeaweedFS");
    expect(text).not.toContain("不防整盘故障");
    expect(text).toContain("datasets.csv");
  });

  it("可用低于下限时标出磁盘空间不足", async () => {
    api.getArchiveOverview.mockResolvedValue(overview({ disk_used_bytes: String(96 * GiB), disk_free_bytes: String(4 * GiB) }));
    const text = await mountPage();
    expect(text).toContain("磁盘空间不足（可用低于下限 5 GB），已暂停领取新运行");
  });

  it("探测失败时写出连不上的起点；起点为 0 时不写", async () => {
    const store = { endpoint: "cos.ap-nanjing.myqcloud.com", bucket: "moox-archive", probe_error: "访问被拒绝" };
    api.getArchiveOverview.mockResolvedValue(
      overview({ object_store: { ...store, unavailable_since: String(Date.parse("2026-11-08T00:00:00Z")) } })
    );
    expect(await mountPage()).toContain("自 2026-11-08 00:00:00 UTC 起连不上");
    wrapper.unmount();
    api.getArchiveOverview.mockResolvedValue(overview({ object_store: { ...store, unavailable_since: "0" } }));
    expect(await mountPage()).not.toContain("起连不上");
  });

  it("未配置对象存储时说明支持本地校验下载", async () => {
    api.getArchiveOverview.mockResolvedValue(overview({ object_store: {} }));
    const text = await mountPage();
    expect(text).toContain("未配置外部对象存储");
    expect(text).toContain("校验下载");
  });

  it("首次读取失败显示原因和重试", async () => {
    api.getArchiveOverview.mockRejectedValue(new Error("connect ECONNREFUSED"));
    const text = await mountPage();
    expect(text).toContain("无法读取归档服务：connect ECONNREFUSED");
    expect(text).toContain("重试");
  });
});
