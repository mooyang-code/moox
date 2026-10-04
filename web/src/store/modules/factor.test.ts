import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { getFactorStatus, listFactorSets } from "@/api/factor";
import type { FactorSetInfo } from "@/api/factor/types";
import { useFactorStore } from "./factor";

vi.mock("@/api/factor", () => ({ listFactorSets: vi.fn(), getFactorStatus: vi.fn() }));

const info = (setId: string, spaceId = "crypto"): FactorSetInfo => ({
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
  factors: []
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
});
