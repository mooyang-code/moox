import { flushPromises, shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  catalog: vi.fn(),
  columns: vi.fn(),
  queryTimeSeries: vi.fn(),
  queryRecords: vi.fn(),
  messageError: vi.fn()
}));

vi.mock("./composables/view-browse-catalog", () => ({ loadTargetedViewCatalog: mocks.catalog }));
vi.mock("@/api/storage/metadata", () => ({
  listDatasetColumns: vi.fn().mockResolvedValue({ columns: [] }),
  listViewColumns: mocks.columns,
  listFields: vi.fn().mockResolvedValue({ fields: [] }),
  listFactors: vi.fn().mockResolvedValue({ factors: [] }),
  listDatasets: vi.fn(),
  listViews: vi.fn(),
  listViewRebuildLogs: vi.fn(),
  requestViewRebuild: vi.fn()
}));
vi.mock("@/api/storage/view", () => ({ queryTimeSeriesRows: mocks.queryTimeSeries, searchRecordRows: mocks.queryRecords }));
vi.mock("@/store/modules/space", () => ({
  useSpaceStore: () => ({ selectedSpaceId: "crypto", requireSpaceId: () => "crypto" })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: mocks.messageError }, Modal: { confirm: vi.fn() } }));

import ViewBrowse from "./index.vue";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => {
    resolve = done;
  });
  return { promise, resolve };
}

function mountPage() {
  const stubs = Object.fromEntries(
    [
      "a-empty",
      "a-tabs",
      "a-tab-pane",
      "a-tag",
      "a-button",
      "a-input",
      "a-dropdown",
      "a-doption",
      "a-table",
      "a-table-column",
      "a-tooltip",
      "a-modal",
      "a-descriptions",
      "a-descriptions-item",
      "a-list",
      "a-list-item",
      "icon-refresh",
      "icon-bar-chart",
      "icon-left",
      "icon-right"
    ].map(name => [name, true])
  );
  return shallowMount(ViewBrowse, {
    props: { embedded: true, viewIds: ["view-known"], activeViewId: "view-known", refreshKey: 0 },
    global: {
      stubs: {
        ...stubs,
        "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
        "a-spin": { template: "<div><slot /></div>" },
        "a-alert": { template: "<div class='alert'><slot /></div>" },
        ResultTable: { props: ["rows"], template: '<div data-testid="rows">{{ rows.map(row => row.key).join(",") }}</div>' }
      }
    }
  });
}

describe("View result refresh", () => {
  beforeEach(() => {
    mocks.catalog.mockReset().mockResolvedValue({
      views: [{ view_id: "view-known", dataset_id: "dataset-known" }],
      datasets: [{ dataset_id: "dataset-known", data_kind: "DATA_KIND_TIME_SERIES" }]
    });
    mocks.columns.mockReset().mockResolvedValue({ columns: [] });
    mocks.queryTimeSeries.mockReset().mockResolvedValue({ rows: [{ key: { subject_id: "BTC", freq: "1m" } }] });
    mocks.queryRecords.mockReset().mockResolvedValue({ rows: [{ key: { record_id: "record-known" } }] });
    mocks.messageError.mockReset();
  });

  it("loads selected View rows again when the parent refresh key changes", async () => {
    const wrapper = mountPage();
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    expect(mocks.queryTimeSeries.mock.lastCall?.[0].view_id).toBe("view-known");
    wrapper.unmount();
  });

  it.each(["time_series", "record"])("retains %s rows and marks them stale after a refresh error", async mode => {
    if (mode === "record") {
      mocks.catalog.mockResolvedValue({
        views: [{ view_id: "view-known", dataset_id: "dataset-known" }],
        datasets: [{ dataset_id: "dataset-known", data_kind: "DATA_KIND_RECORD" }]
      });
    }
    const query = mode === "record" ? mocks.queryRecords : mocks.queryTimeSeries;
    const wrapper = mountPage();
    await flushPromises();
    const previousRows = wrapper.get("[data-testid='rows']").text();
    expect(previousRows).toBe(mode === "record" ? "record-known" : "BTC");
    query.mockRejectedValueOnce(new Error("Storage unavailable"));
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(wrapper.get("[data-testid='rows']").text()).toBe(previousRows);
    expect(wrapper.text()).toContain("Storage unavailable");
    expect(wrapper.text()).toContain("保留上次查询数据");
    expect(wrapper.text()).toContain("数据状态待刷新");

    await wrapper.setProps({ refreshKey: 2 });
    await flushPromises();
    expect(wrapper.text()).not.toContain("Storage unavailable");
    expect(wrapper.text()).not.toContain("数据状态待刷新");
    wrapper.unmount();
  });

  it("leaves an initial query failure empty without claiming previous data", async () => {
    mocks.queryTimeSeries.mockRejectedValue(new Error("Storage unavailable"));
    const wrapper = mountPage();
    await flushPromises();
    expect(wrapper.get("[data-testid='rows']").text()).toBe("");
    expect(wrapper.text()).toContain("Storage unavailable");
    expect(wrapper.text()).not.toContain("保留上次查询数据");
    wrapper.unmount();
  });

  it("queues a refresh requested while the View context is still loading", async () => {
    const columns = deferred<{ columns: [] }>();
    mocks.columns.mockReturnValue(columns.promise);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.setProps({ refreshKey: 1 });
    expect(mocks.queryTimeSeries).not.toHaveBeenCalled();
    columns.resolve({ columns: [] });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("queues a refresh requested while the View catalog is still loading", async () => {
    const catalog = deferred<{
      views: { view_id: string; dataset_id: string }[];
      datasets: { dataset_id: string; data_kind: string }[];
    }>();
    mocks.catalog.mockReturnValueOnce(catalog.promise);
    const wrapper = mountPage();
    await wrapper.setProps({ refreshKey: 1 });
    expect(mocks.queryTimeSeries).not.toHaveBeenCalled();
    catalog.resolve({
      views: [{ view_id: "view-known", dataset_id: "dataset-known" }],
      datasets: [{ dataset_id: "dataset-known", data_kind: "DATA_KIND_TIME_SERIES" }]
    });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("keeps an initially unavailable View catalog error visible and retries it", async () => {
    mocks.catalog.mockRejectedValueOnce(new Error("Storage unavailable"));
    const wrapper = mountPage();
    await flushPromises();
    expect(mocks.queryTimeSeries).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("Storage unavailable");
    await wrapper
      .findAll("button")
      .find(button => button.text().includes("重试"))
      ?.trigger("click");
    await flushPromises();
    expect(mocks.catalog).toHaveBeenCalledTimes(2);
    expect(wrapper.get("[data-testid='rows']").text()).toBe("BTC");
    expect(wrapper.text()).not.toContain("Storage unavailable");
    wrapper.unmount();
  });

  it("queues another refresh during an in-flight row query", async () => {
    const wrapper = mountPage();
    await flushPromises();
    const rows = deferred<{ rows: [] }>();
    mocks.queryTimeSeries.mockReturnValueOnce(rows.promise);
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    await wrapper.setProps({ refreshKey: 2 });
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(2);
    rows.resolve({ rows: [] });
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(3);
    wrapper.unmount();
  });
});
