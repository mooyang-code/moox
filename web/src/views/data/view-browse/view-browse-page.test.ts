import { flushPromises, shallowMount } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  catalog: vi.fn(),
  columns: vi.fn(),
  queryTimeSeries: vi.fn(),
  queryRecords: vi.fn(),
  viewState: vi.fn(),
  messageError: vi.fn()
}));

vi.mock("./composables/view-browse-catalog", () => ({ loadTargetedViewCatalog: mocks.catalog }));
vi.mock("@/api/storage/metadata", () => ({
  listDatasetColumns: vi.fn().mockResolvedValue({ columns: [] }),
  listAllViewColumns: mocks.columns,
  listFields: vi.fn().mockResolvedValue({ fields: [] }),
  listFactors: vi.fn().mockResolvedValue({ factors: [] }),
  listAllDatasets: vi.fn(),
  listAllViews: vi.fn()
}));
vi.mock("@/api/storage/view", async importOriginal => ({
  isViewNotFoundError: (await importOriginal<typeof import("@/api/storage/view")>()).isViewNotFoundError,
  getViewState: mocks.viewState,
  queryTimeSeriesRows: mocks.queryTimeSeries,
  searchRecordRows: mocks.queryRecords
}));
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
    props: { viewIds: ["view-known"], activeViewId: "view-known", refreshKey: 0 },
    global: {
      stubs: {
        ...stubs,
        "a-button": { emits: ["click"], template: "<button @click=\"$emit('click')\"><slot /></button>" },
        "a-spin": { template: "<div><slot /></div>" },
        "a-alert": { template: "<div class='alert'><slot /></div>" }
      }
    }
  });
}

/** 表格当前显示的行（按 key 拼接）。 */
function rowKeys(wrapper: { vm: unknown }): string {
  return ((wrapper.vm as { tableRows: { key: string }[] }).tableRows || []).map(row => row.key).join(",");
}

describe("View result refresh", () => {
  beforeEach(() => {
    mocks.catalog.mockReset().mockResolvedValue({
      views: [{ view_id: "view-known", dataset_id: "dataset-known" }],
      datasets: [{ dataset_id: "dataset-known", data_kind: "DATA_KIND_TIME_SERIES" }]
    });
    mocks.columns.mockReset().mockResolvedValue([]);
    mocks.queryTimeSeries.mockReset().mockResolvedValue({ rows: [{ key: { subject_id: "BTC", freq: "1m" } }] });
    mocks.queryRecords.mockReset().mockResolvedValue({ rows: [{ key: { record_id: "record-known" } }] });
    mocks.viewState.mockReset().mockResolvedValue({ index_id: "index-a", columns: [] });
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
    const previousRows = rowKeys(wrapper);
    expect(previousRows).toBe(mode === "record" ? "record-known" : "BTC");
    query.mockRejectedValueOnce(new Error("Storage unavailable"));
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(rowKeys(wrapper)).toBe(previousRows);
    expect(wrapper.text()).toContain("Storage unavailable");
    expect(wrapper.text()).toContain("保留上次查询数据");
    expect(wrapper.text()).toContain("数据状态待刷新");

    await wrapper.setProps({ refreshKey: 2 });
    await flushPromises();
    expect(wrapper.text()).not.toContain("Storage unavailable");
    expect(wrapper.text()).not.toContain("数据状态待刷新");
    wrapper.unmount();
  });

  it("自动刷新失败不弹提示、错误条不闪；手动刷新失败只由页面提示一次", async () => {
    vi.useFakeTimers();
    const wrapper = shallowMount(ViewBrowse, {
      props: {
        viewIds: ["view-known"],
        activeViewId: "view-known",
        refreshKey: 0,
        autoRefreshIntervalMs: 60_000
      },
      global: {
        stubs: {
          "a-alert": { template: "<div class='alert'><slot /></div>" }
        }
      }
    });
    await vi.advanceTimersByTimeAsync(0);
    await flushPromises();
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(1);
    expect(mocks.queryTimeSeries.mock.lastCall?.[1]).toEqual({ silentError: true });

    // 手动刷新失败：页面提示一次（请求层静默，不会弹两次），错误条出现
    mocks.queryTimeSeries.mockRejectedValueOnce(new Error("Storage unavailable"));
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(mocks.messageError).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("Storage unavailable");

    // 自动刷新再失败：不弹提示，请求开始时也不清掉错误条
    let rejectPoll!: (error: Error) => void;
    mocks.queryTimeSeries.mockImplementationOnce(() => new Promise((_resolve, reject) => (rejectPoll = reject)));
    await vi.advanceTimersByTimeAsync(60_000);
    expect(wrapper.text()).toContain("Storage unavailable");
    rejectPoll(new Error("Storage unavailable"));
    await flushPromises();
    expect(mocks.messageError).toHaveBeenCalledTimes(1);

    // 自动刷新成功后错误条消失
    await vi.advanceTimersByTimeAsync(60_000);
    await flushPromises();
    expect(wrapper.text()).not.toContain("Storage unavailable");
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("视图元数据没读到时自动刷新不重试，不会每轮弹提示", async () => {
    vi.useFakeTimers();
    mocks.catalog.mockRejectedValue(new Error("Storage unavailable"));
    const wrapper = shallowMount(ViewBrowse, {
      props: { viewIds: ["view-known"], activeViewId: "view-known", autoRefreshIntervalMs: 60_000 },
      global: { stubs: { "a-alert": { template: "<div class='alert'><slot /></div>" } } }
    });
    await vi.advanceTimersByTimeAsync(0);
    await flushPromises();
    const calls = mocks.catalog.mock.calls.length;
    const errors = mocks.messageError.mock.calls.length;
    await vi.advanceTimersByTimeAsync(120_000);
    expect(mocks.catalog.mock.calls.length).toBe(calls);
    expect(mocks.messageError.mock.calls.length).toBe(errors);
    wrapper.unmount();
    vi.useRealTimers();
  });

  it("行操作“K 线”用这一行的标的和序列标签查，不用查询区的筛选", async () => {
    const wrapper = mountPage();
    await flushPromises();
    const vm = wrapper.vm as unknown as {
      filters: { fieldName: string; operator: string; value?: string }[];
      openRowKline(record: unknown): Promise<void>;
      reloadKlineRecords(): Promise<void>;
    };
    // 查询区筛的是另一个标的：行内 K 线不能用它
    vm.filters.splice(0, vm.filters.length, { fieldName: "subject_id", operator: "eq", value: "BTCUSDT" });
    mocks.queryTimeSeries.mockClear();
    await vm.openRowKline({ rawKey: { subject_id: "ETHUSDT", series_tag: "market:swap" } });
    await flushPromises();
    const filter = JSON.stringify(mocks.queryTimeSeries.mock.calls[0]?.[0]?.filter);
    expect(filter).toContain("ETHUSDT");
    expect(filter).toContain("market:swap");
    expect(filter).not.toContain("BTCUSDT");
    // 弹窗里刷新也沿用这一行
    mocks.queryTimeSeries.mockClear();
    await vm.reloadKlineRecords();
    await flushPromises();
    expect(JSON.stringify(mocks.queryTimeSeries.mock.calls[0]?.[0]?.filter)).toContain("ETHUSDT");
    wrapper.unmount();
  });

  it("leaves an initial query failure empty without claiming previous data", async () => {
    mocks.queryTimeSeries.mockRejectedValue(new Error("Storage unavailable"));
    const wrapper = mountPage();
    await flushPromises();
    expect(rowKeys(wrapper)).toBe("");
    expect(wrapper.text()).toContain("Storage unavailable");
    expect(wrapper.text()).not.toContain("保留上次查询数据");
    wrapper.unmount();
  });

  it("queues a refresh requested while the View context is still loading", async () => {
    const columns = deferred<never[]>();
    mocks.columns.mockReturnValue(columns.promise);
    const wrapper = mountPage();
    await flushPromises();
    await wrapper.setProps({ refreshKey: 1 });
    expect(mocks.queryTimeSeries).not.toHaveBeenCalled();
    columns.resolve([]);
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
    expect(rowKeys(wrapper)).toBe("BTC");
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

describe("View 索引状态", () => {
  beforeEach(() => {
    mocks.catalog.mockReset().mockResolvedValue({
      views: [{ view_id: "view-known", dataset_id: "dataset-known" }],
      datasets: [{ dataset_id: "dataset-known", data_kind: "DATA_KIND_TIME_SERIES" }]
    });
    mocks.columns.mockReset().mockResolvedValue([{ column_name: "open" }, { column_name: "close" }]);
    mocks.queryTimeSeries.mockReset().mockResolvedValue({ rows: [] });
    mocks.viewState.mockReset();
    mocks.messageError.mockReset();
  });

  function tagText(wrapper: { vm: unknown }) {
    return (wrapper.vm as { viewIndexTag: { text: string } }).viewIndexTag.text;
  }

  it("从 GetViewState 读出已构建、已建列与覆盖范围", async () => {
    mocks.viewState.mockResolvedValue({
      index_id: "index-a",
      columns: [{ column_name: "open" }],
      indexed_from: "2026-01-01T00:00:00Z",
      indexed_to: "2026-01-02T00:00:00Z"
    });
    const wrapper = mountPage();
    await flushPromises();
    expect(mocks.viewState).toHaveBeenCalledWith({ space_id: "crypto", view_id: "view-known" }, { silentError: true });
    expect(tagText(wrapper)).toBe("已构建");
    expect(wrapper.text()).toContain("已建列: 1/2");
    expect(wrapper.text()).toContain("覆盖: 2026-01-01 00:00:00");
    wrapper.unmount();
  });

  it("索引为空显示构建中，VIEW_NOT_FOUND 显示未构建，其他失败显示未知且不影响查询", async () => {
    mocks.viewState.mockResolvedValueOnce({ index_id: "" });
    const wrapper = mountPage();
    await flushPromises();
    expect(tagText(wrapper)).toBe("构建中");
    expect(wrapper.text()).not.toContain("已建列");

    mocks.viewState.mockRejectedValueOnce(Object.assign(new Error("not found"), { retCode: "VIEW_NOT_FOUND" }));
    await wrapper.setProps({ refreshKey: 1 });
    await flushPromises();
    expect(tagText(wrapper)).toBe("未构建");

    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mocks.viewState.mockRejectedValueOnce(new Error("Storage unavailable"));
    await wrapper.setProps({ refreshKey: 2 });
    await flushPromises();
    expect(tagText(wrapper)).toBe("索引状态未知");
    expect(mocks.queryTimeSeries).toHaveBeenCalledTimes(3);
    expect(mocks.messageError).not.toHaveBeenCalled();
    warn.mockRestore();
    wrapper.unmount();
  });
});
