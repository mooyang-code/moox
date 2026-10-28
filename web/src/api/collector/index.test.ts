import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ callControl: vi.fn() }));

vi.mock("@/api/admin/http", () => ({ callControl: mocks.callControl }));

import { listSubjectListers, listTagSyncStatus } from "./index";

describe("Collector 标签同步接口", () => {
  beforeEach(() => mocks.callControl.mockReset());

  it("按空间读取标签同步状态，空空间表示所有空间，没有记录时返回空数组", async () => {
    mocks.callControl.mockResolvedValue({ items: [{ space_id: "crypto", tag_id: "crypto_spot", last_status: "success" }] });
    expect(await listTagSyncStatus("crypto")).toEqual([{ space_id: "crypto", tag_id: "crypto_spot", last_status: "success" }]);
    expect(mocks.callControl).toHaveBeenLastCalledWith("collector", "ListTagSyncStatus", { space_id: "crypto" });

    mocks.callControl.mockResolvedValue({});
    expect(await listTagSyncStatus()).toEqual([]);
    expect(mocks.callControl).toHaveBeenLastCalledWith("collector", "ListTagSyncStatus", { space_id: "" });
  });

  it("读取可用的来源与市场类型组合", async () => {
    mocks.callControl.mockResolvedValue({ listers: [{ source: "binance", market_types: ["spot", "swap"] }] });
    expect(await listSubjectListers()).toEqual([{ source: "binance", market_types: ["spot", "swap"] }]);
    expect(mocks.callControl).toHaveBeenLastCalledWith("collector", "ListSubjectListers", {});

    mocks.callControl.mockResolvedValue({});
    expect(await listSubjectListers()).toEqual([]);
  });
});
