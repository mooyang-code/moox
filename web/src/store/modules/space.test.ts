import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { listSpaces } from "@/api/admin/spaces";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { useSpaceStore } from "./space";

vi.mock("@/api/admin/spaces", () => ({
  listSpaces: vi.fn()
}));
vi.mock("@/api/admin/space-header", () => ({
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
      spaces: [{ space_id: "stock_cn", name: "A股市场", status: "active" }]
    });

    const store = useSpaceStore();
    await store.loadSpaces();

    expect(listSpaces).toHaveBeenCalledWith({
      status: "active",
      page: { page: 1, size: 200 }
    });
    expect(store.selectedSpaceId).toBe("stock_cn");
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
});
