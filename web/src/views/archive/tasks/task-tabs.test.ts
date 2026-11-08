import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ArchivePartition } from "@/api/archive";

const api = vi.hoisted(() => ({
  listArchivePartitions: vi.fn(),
  downloadArchivePartition: vi.fn(),
  saveBlob: vi.fn(),
  fetchArchiveRecentRows: vi.fn(),
  listSubjects: vi.fn(),
  error: vi.fn()
}));
vi.mock("@/api/archive", () => ({
  listArchivePartitions: api.listArchivePartitions,
  downloadArchivePartition: api.downloadArchivePartition,
  saveBlob: api.saveBlob,
  fetchArchiveRecentRows: api.fetchArchiveRecentRows
}));
vi.mock("@/api/storage/metadata", () => ({ listSubjects: api.listSubjects }));
vi.mock("@arco-design/web-vue", async importOriginal => {
  const actual = await importOriginal<typeof import("@arco-design/web-vue")>();
  return { ...actual, Message: { ...actual.Message, error: api.error, success: vi.fn(), warning: vi.fn() } };
});
import TaskPartitions from "./task-partitions.vue";
import TaskRecent from "./task-recent.vue";

const partition = (over: Partial<ArchivePartition> = {}): ArchivePartition => ({
  partition_key: "2026-11-07",
  status: "final",
  file_path: "crypto/collect/binance-spot-kline-1m/2026/20261107.csv.gz",
  remote_state: "verified",
  ...over
});

let wrapper: VueWrapper;
beforeEach(() => {
  api.listArchivePartitions.mockReset();
  api.downloadArchivePartition.mockReset();
  api.saveBlob.mockReset();
  api.fetchArchiveRecentRows.mockReset();
  api.listSubjects.mockReset().mockResolvedValue({ subjects: [] });
  api.error.mockReset();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("分区下载", () => {
  const query = {
    task: "ds",
    tab: "partitions",
    status: "",
    ticks: false,
    rpage: 1,
    ppage: 1,
    from: "",
    to: "",
    partition: "",
    subject: "",
    create: ""
  } as const;

  async function mountPartitions(partitions: ArchivePartition[]) {
    api.listArchivePartitions.mockResolvedValue({ partitions, page_result: { total: partitions.length } });
    wrapper = mount(TaskPartitions, {
      props: {
        task: { dataset_id: "ds", dir_name: "binance-spot-kline-1m" },
        spaceId: "crypto",
        query,
        zone: "UTC",
        remoteEnabled: true
      },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    await flushPromises();
    return wrapper.findAll("button").filter(button => ["下载", "下载中"].includes(button.text()) || button.text().endsWith("%"));
  }

  it("远端已校验的可下载，未就绪的置灰并在悬停里写原因；文件列显示新的相对路径", async () => {
    const [ready, pending] = await mountPartitions([
      partition(),
      partition({ partition_key: "2026-11-08", remote_state: "pending" })
    ]);
    expect(wrapper.text()).toContain("crypto/collect/binance-spot-kline-1m/2026/20261107.csv.gz");
    expect(ready.attributes("disabled")).toBeUndefined();
    expect(pending.attributes("disabled")).toBeDefined();
    expect(pending.element.parentElement?.getAttribute("title")).toBe("远端副本还没有上传或校验");
  });

  it("点下载：按空间、数据集、分区下载，交给浏览器保存；失败时提示执行器给的原因", async () => {
    const blob = new Blob(["x"]);
    api.downloadArchivePartition.mockResolvedValueOnce({ blob, fileName: "a.csv.gz" });
    const [ready] = await mountPartitions([partition()]);
    await ready.trigger("click");
    await flushPromises();
    expect(api.downloadArchivePartition).toHaveBeenCalledWith(
      { space: "crypto", dataset: "ds", partition: "2026-11-07", fallbackName: "binance-spot-kline-1m_2026-11-07.csv.gz" },
      expect.any(Function)
    );
    expect(api.saveBlob).toHaveBeenCalledWith(blob, "a.csv.gz");

    api.downloadArchivePartition.mockRejectedValueOnce(new Error("同时下载的数量已达上限（2 个），请稍后再试"));
    await ready.trigger("click");
    await flushPromises();
    expect(api.error).toHaveBeenCalledWith("下载 2026-11-07 失败：同时下载的数量已达上限（2 个），请稍后再试");
  });
});

describe("最新数据", () => {
  const columns = [
    { name: "data_time", type: "time" },
    { name: "subject_id", type: "string" },
    { name: "series_tag", type: "string" },
    { name: "close", type: "double" },
    { name: "note", type: "string" }
  ];
  const row = (time: string): (string | null)[] => [time, "BTC", "", "1.5", null];

  function mountRecent(subject = "BTC") {
    wrapper = mount(TaskRecent, {
      props: { spaceId: "crypto", datasetId: "ds", subject, zone: "UTC" },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    return wrapper;
  }

  it("按标的读取，表头写已完整读取到的时刻，null 与空串分开显示", async () => {
    api.fetchArchiveRecentRows.mockResolvedValue({
      columns,
      rows: [row("2026-11-07T04:40:00Z")],
      staged_until: "2026-11-07T04:41:00Z"
    });
    mountRecent();
    await flushPromises();
    expect(api.fetchArchiveRecentRows).toHaveBeenCalledWith(
      { space: "crypto", dataset: "ds", subject: "BTC", before: undefined, limit: 200 },
      expect.any(AbortSignal)
    );
    const text = wrapper.text();
    expect(text).toContain("暂存库已完整读取到 2026-11-07 04:41:00 UTC");
    expect(text).not.toContain("暂存到");
    expect(text).toContain('""');
    expect(text).toContain("—");
    // 不满一页：没有更早的行
    expect(
      wrapper
        .findAll("button")
        .find(button => button.text() === "更早")
        ?.attributes("disabled")
    ).toBeDefined();
  });

  it("满一页时“更早”用最后一行的时间翻页，“回到最新”不带 before", async () => {
    const page = Array.from({ length: 200 }, (_, i) => row(new Date(Date.UTC(2026, 10, 7, 4, 40 - i)).toISOString()));
    api.fetchArchiveRecentRows.mockResolvedValue({ columns, rows: page, staged_until: "" });
    mountRecent();
    await flushPromises();
    await wrapper
      .findAll("button")
      .find(button => button.text() === "更早")!
      .trigger("click");
    await flushPromises();
    expect(api.fetchArchiveRecentRows).toHaveBeenLastCalledWith(
      expect.objectContaining({ before: page[199][0] }),
      expect.any(AbortSignal)
    );
    await wrapper
      .findAll("button")
      .find(button => button.text() === "回到最新")!
      .trigger("click");
    await flushPromises();
    expect(api.fetchArchiveRecentRows).toHaveBeenLastCalledWith(
      expect.objectContaining({ before: undefined }),
      expect.any(AbortSignal)
    );
  });

  it("读取失败显示原因与重试；没选标的时不读取", async () => {
    api.fetchArchiveRecentRows.mockRejectedValue(new Error("数据集说明（dataset.json）还没有生成，等任务首次运行后再查看"));
    mountRecent();
    await flushPromises();
    expect(wrapper.text()).toContain("读取最新数据失败：数据集说明（dataset.json）还没有生成");
    wrapper.unmount();
    api.fetchArchiveRecentRows.mockClear();
    mountRecent("");
    await flushPromises();
    expect(api.fetchArchiveRecentRows).not.toHaveBeenCalled();
    expect(wrapper.text()).toContain("选一个标的");
  });
});
