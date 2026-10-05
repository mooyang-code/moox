import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { getFactorStatus, listFactorSets } from "@/api/factor";
import { listDatasets } from "@/api/storage/metadata";
import type { FactorSetInfo } from "@/api/factor/types";
import { useFactorStore } from "./factor";

vi.mock("@/api/factor", () => ({ listFactorSets: vi.fn(), getFactorStatus: vi.fn() }));
vi.mock("@/api/storage/metadata", () => ({ listDatasets: vi.fn() }));

const info = (setId: string, spaceId = "crypto", members: FactorSetInfo["members"] = []): FactorSetInfo => ({
  factor_set: {
    set_id: setId,
    space_id: spaceId,
    source_dataset_id: "d",
    freq: "1m",
    subject_mode: "all",
    subjects: [],
    result_dataset_id: `dataset_factor_${setId}`,
    status: "enabled"
  },
  members
});

const datasetPage = (datasets: Array<{ dataset_id: string; name: string }>) => ({
  ret_info: { code: 0, msg: "" },
  datasets,
  page_result: { page: 1, size: 500, total: datasets.length, has_more: false, next_cursor: "" }
});

const page = (sets: FactorSetInfo[], hasMore = false) => ({
  ret_info: { code: 0, msg: "" },
  factor_sets: sets,
  page_result: { page: 1, size: 500, total: sets.length, has_more: hasMore, next_cursor: "" }
});

describe("factor store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.mocked(listFactorSets).mockReset();
    vi.mocked(listDatasets).mockReset();
    vi.mocked(listDatasets).mockResolvedValue(datasetPage([]) as never);
  });

  it("loads every page and keeps only the requested space", async () => {
    vi.mocked(listFactorSets)
      .mockResolvedValueOnce(page([info("a"), info("x", "stockcn")], true))
      .mockResolvedValueOnce(page([info("b")]));
    const store = useFactorStore();
    await store.load("crypto");
    expect(store.sets.map(item => item.factor_set.set_id)).toEqual(["a", "b"]);
    expect(store.currentSetId).toBe("a");
  });

  it("keeps a still-valid selection and falls back when it disappears", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a"), info("b")]));
    const store = useFactorStore();
    await store.load("crypto");
    store.select("b");
    await store.load("crypto");
    expect(store.currentSetId).toBe("b");

    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    await store.load("crypto");
    expect(store.currentSetId).toBe("a");
  });

  it("ignores unknown ids in select", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    const store = useFactorStore();
    await store.load("crypto");
    store.select("missing");
    expect(store.currentSetId).toBe("a");
  });

  it("drops responses that were superseded by a newer load", async () => {
    let releaseFirst: (value: ReturnType<typeof page>) => void = () => undefined;
    vi.mocked(listFactorSets)
      .mockReturnValueOnce(
        new Promise(resolve => {
          releaseFirst = resolve as typeof releaseFirst;
        }) as never
      )
      .mockResolvedValueOnce(page([info("fresh", "stockcn")]));
    const store = useFactorStore();
    const stale = store.load("crypto");
    await store.load("stockcn");
    releaseFirst(page([info("stale")]));
    await stale;
    expect(store.sets.map(item => item.factor_set.set_id)).toEqual(["fresh"]);
  });

  it("reports load failures without clearing existing data", async () => {
    vi.mocked(listFactorSets).mockResolvedValueOnce(page([info("a")]));
    const store = useFactorStore();
    await store.load("crypto");
    vi.mocked(listFactorSets).mockRejectedValueOnce(new Error("boom"));
    await store.load("crypto");
    expect(store.loadError).toBe("boom");
    expect(store.sets).toHaveLength(1);
  });

  it("silent reloads do not toggle the loading flag", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    const store = useFactorStore();
    const seen: boolean[] = [];
    const stop = store.$subscribe(() => seen.push(store.loading), { flush: "sync" });
    await store.load("crypto", { silent: true });
    stop();
    expect(seen).not.toContain(true);
  });

  it("keeps engine status null when the status call fails", async () => {
    vi.mocked(getFactorStatus).mockRejectedValue(new Error("down"));
    const store = useFactorStore();
    await store.refreshEngine();
    expect(store.engine).toBeNull();
  });

  it("clears everything when no space is selected", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    const store = useFactorStore();
    await store.load("crypto");
    await store.load("");
    expect(store.sets).toEqual([]);
    expect(store.currentSetId).toBe("");
  });

  it("datasetNames maps dataset id to name per space", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    vi.mocked(listDatasets).mockResolvedValueOnce(datasetPage([{ dataset_id: "d", name: "现货K线" }]) as never);
    const store = useFactorStore();
    await store.load("crypto");
    expect(store.datasetNames).toEqual({ d: "现货K线" });
    expect(listDatasets).toHaveBeenCalledWith(expect.objectContaining({ space_id: "crypto" }));

    // 同一空间的后续刷新不重复拉取；切到另一个空间则重新加载并替换映射
    await store.reload();
    expect(listDatasets).toHaveBeenCalledTimes(1);
    vi.mocked(listDatasets).mockResolvedValueOnce(datasetPage([{ dataset_id: "e", name: "A股日线" }]) as never);
    await store.load("stockcn");
    expect(store.datasetNames).toEqual({ e: "A股日线" });
  });

  it("setLabel falls back to source_dataset_id", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    vi.mocked(listDatasets).mockResolvedValueOnce(datasetPage([{ dataset_id: "d", name: "现货K线" }]) as never);
    const store = useFactorStore();
    await store.load("crypto");
    const set = store.sets[0].factor_set;
    expect(store.setLabel(set)).toBe("现货K线 · 1m");
    expect(store.setLabel({ ...set, source_dataset_id: "unknown" })).toBe("unknown · 1m");
  });

  it("keeps a failed dataset-name lookup from failing the set load", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    vi.mocked(listDatasets).mockRejectedValueOnce(new Error("meta down"));
    const store = useFactorStore();
    await store.load("crypto");
    expect(store.loadError).toBe("");
    expect(store.setLabel(store.sets[0].factor_set)).toBe("d · 1m");
  });

  it("reload keeps currentSetId when still present", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a"), info("b")]));
    const store = useFactorStore();
    await store.load("crypto");
    store.select("b");
    await store.reload();
    expect(store.currentSetId).toBe("b");
  });

  it("members replace factors on FactorSetInfo", async () => {
    const member = {
      set_id: "a",
      factor_id: "Bias",
      status: "enabled" as const,
      factor: { factor_id: "Bias" } as never,
      created_at: "",
      updated_at: ""
    };
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a", "crypto", [member])]));
    const store = useFactorStore();
    await store.load("crypto");
    expect(store.sets[0].members.map(item => item.factor_id)).toEqual(["Bias"]);
    expect(store.sets[0]).not.toHaveProperty("factors");
  });

  it("tracks which space has been loaded", async () => {
    vi.mocked(listFactorSets).mockResolvedValue(page([info("a")]));
    const store = useFactorStore();
    expect(store.isLoadedFor("crypto")).toBe(false);
    await store.load("crypto");
    expect(store.isLoadedFor("crypto")).toBe(true);
    expect(store.isLoadedFor("stockcn")).toBe(false);
    store.reset();
    expect(store.isLoadedFor("crypto")).toBe(false);
  });
});
