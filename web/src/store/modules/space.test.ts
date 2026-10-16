import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { listSpaces } from "@/api/admin/spaces";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { isSystemSpace, useSpaceStore } from "./space";

vi.mock("@/api/admin/spaces", () => ({
  listSpaces: vi.fn()
}));
vi.mock("@/api/admin/space-header", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/admin/space-header")>()),
  setSelectedSpaceIdCache: vi.fn()
}));

describe("space store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.mocked(listSpaces).mockReset();
    vi.mocked(setSelectedSpaceIdCache).mockReset();
  });

  it("only loads active Spaces for the global selector", async () => {
    vi.mocked(listSpaces).mockResolvedValue({
      spaces: [{ space_id: "stockcn", name: "A股市场", status: "active" }]
    });

    const store = useSpaceStore();
    await store.loadSpaces();

    expect(listSpaces).toHaveBeenCalledWith({
      status: "active",
      page: { page: 1, size: 200 }
    });
    expect(store.selectedSpaceId).toBe("stockcn");
  });

  it("leaves internal mooxsys spaces out of the selector", async () => {
    vi.mocked(listSpaces).mockResolvedValue({
      spaces: [
        { space_id: "mooxsys", name: "系统数据", status: "active" },
        { space_id: "mooxsys_ops", name: "系统运维", status: "active" },
        { space_id: "crypto", name: "加密货币市场", status: "active" }
      ]
    });
    const store = useSpaceStore();
    store.selectedSpaceId = "mooxsys";
    await store.loadSpaces();

    expect(store.spaces.map(space => space.space_id)).toEqual(["crypto"]);
    // 之前选中的系统空间回到第一个交易市场空间。
    expect(store.selectedSpaceId).toBe("crypto");
    store.setSelectedSpace("mooxsys");
    expect(store.selectedSpaceId).toBe("crypto");
    expect(isSystemSpace("MooxSys")).toBe(true);
    expect(isSystemSpace("stockcn")).toBe(false);
  });

  it("rejects a Space that is not in the active selector list", async () => {
    vi.mocked(listSpaces).mockResolvedValue({
      spaces: [{ space_id: "crypto", name: "加密货币市场", status: "active" }]
    });
    const store = useSpaceStore();
    await store.loadSpaces();
    vi.mocked(setSelectedSpaceIdCache).mockClear();

    store.setSelectedSpace("disabled_space");

    expect(store.selectedSpaceId).toBe("crypto");
    expect(setSelectedSpaceIdCache).not.toHaveBeenCalled();
  });

  it("makes concurrent loaders wait for the same in-flight request", async () => {
    let finish!: (value: { spaces: Array<{ space_id: string; name: string; status: string }> }) => void;
    vi.mocked(listSpaces).mockReturnValue(
      new Promise(resolve => {
        finish = resolve;
      }) as ReturnType<typeof listSpaces>
    );
    const store = useSpaceStore();
    const first = store.loadSpaces();
    let secondDone = false;
    const second = store.loadSpaces().then(() => {
      secondDone = true;
    });
    await Promise.resolve();
    expect(secondDone).toBe(false);
    expect(listSpaces).toHaveBeenCalledTimes(1);

    finish({ spaces: [{ space_id: "crypto", name: "加密货币市场", status: "active" }] });
    await Promise.all([first, second]);
    expect(secondDone).toBe(true);
    expect(store.selectedSpaceId).toBe("crypto");
  });
});
