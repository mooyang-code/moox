import { flushPromises, mount } from "@vue/test-utils";
import { defineComponent, h, KeepAlive, reactive, ref } from "vue";
import { beforeEach, describe, expect, it, vi } from "vitest";

// 页签容器用 keep-alive 缓存“采集结果”：停用后切空间，里面的结果浏览不能拿旧空间的数据集 ID 去新空间读元数据。
const mocks = vi.hoisted(() => ({
  getTaskList: vi.fn(),
  getTaskDetail: vi.fn(),
  getDataset: vi.fn(),
  columns: vi.fn(),
  messageError: vi.fn(),
  routeQuery: {} as Record<string, unknown>,
  selectedSpaceId: undefined as { value: string } | undefined
}));

vi.mock("@/api/collector", () => ({
  GetTaskList: mocks.getTaskList,
  GetTaskDetail: mocks.getTaskDetail,
  listKlineResampleJobs: vi.fn().mockResolvedValue([])
}));
vi.mock("@/api/storage/metadata", () => ({
  getDataset: mocks.getDataset,
  listAllDatasetColumns: mocks.columns
}));
vi.mock("@/api/storage/view", () => ({
  isViewNotFoundError: () => false,
  getViewState: vi.fn().mockResolvedValue({ index_id: "index", columns: [] }),
  queryTimeSeriesRows: vi.fn().mockResolvedValue({ rows: [] }),
  searchRecordRows: vi.fn().mockResolvedValue({ rows: [] })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { error: mocks.messageError, info: vi.fn() }, Modal: { confirm: vi.fn() } }));
vi.mock("@/store/modules/space", async () => {
  const { ref } = await import("vue");
  mocks.selectedSpaceId = ref("crypto");
  return {
    useSpaceStore: () => ({
      get selectedSpaceId() {
        return mocks.selectedSpaceId?.value || "";
      },
      selectedSpace: undefined,
      requireSpaceId: () => mocks.selectedSpaceId?.value || ""
    })
  };
});
vi.mock("vue-router", () => ({
  useRoute: () => ({ query: mocks.routeQuery }),
  useRouter: () => ({ replace: vi.fn().mockResolvedValue(undefined) })
}));

import TaskResults from "./index.vue";

/** 每个空间一个就绪的采集任务，结果数据集只属于本空间。 */
function taskOf(spaceId: string) {
  return {
    space_id: spaceId,
    task_id: `task-${spaceId}`,
    task_name: `${spaceId} K 线`,
    created_at: "1790812800000",
    result: { dataset_id: `dataset-${spaceId}`, status: "ready" }
  };
}

const stubs = {
  ...Object.fromEntries(
    ["a-button", "a-empty", "a-tab-pane", "a-tag", "QueryControls", "ResultTable", "KlineModal"].map(name => [name, true])
  ),
  "a-alert": { template: "<div class='alert'><slot /></div>" },
  "a-spin": { template: "<div><slot /></div>" },
  "a-tabs": { template: "<div><slot /></div>" }
};

function mountTabs() {
  const tab = ref<"results" | "tasks">("results");
  const OtherTab = defineComponent({ name: "OtherTab", render: () => h("div", "采集任务") });
  const Host = defineComponent({
    setup: () => () => h(KeepAlive, null, [tab.value === "results" ? h(TaskResults) : h(OtherTab)])
  });
  return { wrapper: mount(Host, { global: { stubs } }), tab };
}

describe("采集结果页签停用后切空间", () => {
  beforeEach(() => {
    if (mocks.selectedSpaceId) mocks.selectedSpaceId.value = "crypto";
    mocks.routeQuery = reactive({ tab: "results" });
    mocks.getTaskList
      .mockReset()
      .mockImplementation(({ space_id }: { space_id: string }) =>
        Promise.resolve({ tasks: [taskOf(space_id)], page_result: { page: 1, size: 100, total: 1 } })
      );
    mocks.getTaskDetail
      .mockReset()
      .mockImplementation(({ space_id }: { space_id: string }) => Promise.resolve({ task: taskOf(space_id) }));
    // 与 Storage 一致：数据集不在请求的空间里时按 ID 取不到行
    mocks.getDataset
      .mockReset()
      .mockImplementation(({ space_id, dataset_id }: { space_id: string; dataset_id: string }) =>
        dataset_id === `dataset-${space_id}`
          ? Promise.resolve({ dataset: { space_id, dataset_id, data_kind: "DATA_KIND_TIME_SERIES" } })
          : Promise.reject(new Error("metadata row not found: sql: no rows in result set"))
      );
    mocks.columns.mockReset().mockResolvedValue([]);
    mocks.messageError.mockReset();
  });

  it("不拿旧空间的数据集去新空间读元数据，切回结果页签时按新空间的任务读取", async () => {
    const { wrapper, tab } = mountTabs();
    await flushPromises();
    expect(mocks.getDataset).toHaveBeenCalledWith({ space_id: "crypto", dataset_id: "dataset-crypto" });

    tab.value = "tasks";
    mocks.routeQuery.tab = undefined;
    await flushPromises();
    mocks.selectedSpaceId!.value = "stockcn";
    await flushPromises();

    expect(mocks.getDataset).not.toHaveBeenCalledWith({ space_id: "stockcn", dataset_id: "dataset-crypto" });
    expect(mocks.messageError).not.toHaveBeenCalled();

    tab.value = "results";
    mocks.routeQuery.tab = "results";
    await flushPromises();

    expect(mocks.getDataset).toHaveBeenLastCalledWith({ space_id: "stockcn", dataset_id: "dataset-stockcn" });
    expect(mocks.messageError).not.toHaveBeenCalled();
    wrapper.unmount();
  });

  it("停用期间切走又切回原空间，再打开结果页签时数据集照常显示", async () => {
    const { wrapper, tab } = mountTabs();
    await flushPromises();

    tab.value = "tasks";
    mocks.routeQuery.tab = undefined;
    await flushPromises();
    mocks.selectedSpaceId!.value = "stockcn";
    await flushPromises();
    mocks.selectedSpaceId!.value = "crypto";
    await flushPromises();
    mocks.getDataset.mockClear();

    tab.value = "results";
    mocks.routeQuery.tab = "results";
    await flushPromises();

    expect(mocks.getDataset).toHaveBeenCalledWith({ space_id: "crypto", dataset_id: "dataset-crypto" });
    expect(mocks.messageError).not.toHaveBeenCalled();
    expect((wrapper.findComponent({ name: "DataViewBrowse" }).vm as unknown as { datasets: unknown[] }).datasets).toHaveLength(1);
    wrapper.unmount();
  });
});
