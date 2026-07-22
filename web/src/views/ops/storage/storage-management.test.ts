import fs from "node:fs";
import path from "node:path";
import { flushPromises, mount } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listDataNodes: vi.fn(),
  push: vi.fn()
}));

vi.mock("@/api/storage/metadata", async () => {
  const actual = await vi.importActual<typeof import("@/api/storage/metadata")>("@/api/storage/metadata");
  return { ...actual, listDataNodes: mocks.listDataNodes };
});

vi.mock("vue-router", () => ({ useRouter: () => ({ push: mocks.push }) }));

import DataNodes from "./nodes.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn().mockImplementation(() => ({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn()
  }))
});

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

const nodeResponse = {
  items: [
    {
      node: {
        node_id: "node-a",
        name: "节点 A",
        service_target: "trpc://storage-a:20200",
        status: "active",
        updated_at: "2026-07-22T10:00:00Z"
      },
      datasets: [
        {
          space_id: "space-a",
          dataset_id: "dataset-a",
          name: "行情数据",
          data_kind: "DATA_KIND_TIME_SERIES",
          keep_duration: "30d",
          status: "active"
        }
      ]
    },
    {
      node: {
        node_id: "node-b",
        name: "节点 B",
        service_target: "trpc://storage-b:20200",
        status: "disabled",
        updated_at: "2026-07-22T10:00:00Z"
      },
      datasets: []
    }
  ],
  page_result: { page: 1, size: 20, total: 2, has_more: false, next_cursor: "" }
};

describe("storage configuration workbench", () => {
  beforeEach(() => {
    mocks.listDataNodes.mockResolvedValue(nodeResponse);
    mocks.push.mockReset();
  });

  it("keeps DataNode and archive pages in one surface and falls back unknown tabs to nodes", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["数据节点", "归档文件"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="存储配置"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).toContain('typeStorageConfigTab="nodes"|"archive"');
    expect(normalized).toContain('returnvalue==="archive"?value:"nodes"');
    expect(normalized).not.toContain("routes");
  });

  it("removes the route page and route URL while keeping the single storage menu", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const metadata = fs.readFileSync(path.resolve(__dirname, "../../../api/storage/metadata.ts"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).toContain('menu("0606","06","/ops/storage/nodes","ops-storage"');
    expect(normalizedRoutes).not.toContain("/ops/storage/routes");
    expect(metadata).not.toContain("PrimaryStore");
    expect(fs.existsSync(path.resolve(__dirname, "routes.vue"))).toBe(false);
  });

  it("renders read-only node identity, wrapped Dataset tags, deep links, and safe delete affordances", async () => {
    const wrapper = mount(DataNodes, {
      global: {
        plugins: [ArcoVue],
        stubs: {
          "icon-eye": true,
          "icon-delete": true,
          "icon-edit": true,
          "icon-info-circle": true
        }
      }
    });
    await flushPromises();
    await flushPromises();

    expect(mocks.listDataNodes).toHaveBeenCalledOnce();
    expect(wrapper.text()).toContain("trpc://storage-a:20200");
    expect(wrapper.text()).toContain("行情数据");
    expect(wrapper.text()).toContain("-");
    expect(wrapper.text()).not.toContain("新增节点");
    expect(wrapper.find(".dataset-tags").exists()).toBe(true);
    expect(wrapper.find(".dataset-tags").attributes("style")).toBeUndefined();
    expect(wrapper.findAll(".dataset-tag")).toHaveLength(1);
    expect(wrapper.findAll('[aria-label="删除数据节点"]')).toHaveLength(2);
    expect(wrapper.findAll('[aria-label="删除数据节点"]').every(button => button.attributes("disabled") !== undefined)).toBe(
      false
    );

    await wrapper.find(".dataset-tag").trigger("click");
    expect(mocks.push).toHaveBeenCalledWith({
      path: "/data/datasets",
      query: { space_id: "space-a", dataset_id: "dataset-a" }
    });
  });
});
