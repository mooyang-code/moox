import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ listFunctionInstances: vi.fn() }));
vi.mock("@/api/function-instance", () => api);
import { cloudAccountDeleteText, instancesUsingAccount } from "./cloud-account-delete";

beforeEach(() => api.listFunctionInstances.mockReset());

describe("删除云账户前统计在用的实例", () => {
  it("逐个空间查询（显式带空间）再相加", async () => {
    api.listFunctionInstances.mockImplementation(async (_params: unknown, spaceId: string) => ({
      items: [],
      total: spaceId === "crypto" ? 2 : 1
    }));
    await expect(instancesUsingAccount("acc-1", ["crypto", "stockcn"])).resolves.toBe(3);
    expect(api.listFunctionInstances).toHaveBeenCalledWith({ cloud_account_id: "acc-1", page_size: 1 }, "crypto");
    expect(api.listFunctionInstances).toHaveBeenCalledWith({ cloud_account_id: "acc-1", page_size: 1 }, "stockcn");
  });

  it("任一空间查询失败或没有空间列表时返回 null，不当成没有实例", async () => {
    api.listFunctionInstances.mockResolvedValueOnce({ items: [], total: 0 }).mockRejectedValueOnce(new Error("超时"));
    await expect(instancesUsingAccount("acc-1", ["crypto", "stockcn"])).resolves.toBeNull();
    await expect(instancesUsingAccount("acc-1", [])).resolves.toBeNull();
  });
});

describe("删除云账户的确认正文", () => {
  it("有实例在用时写出个数和后果，并让人先删实例", () => {
    const text = cloudAccountDeleteText(3);
    expect(text).toContain("所有空间里还有 3 个执行器实例在用");
    expect(text).toContain("在管理台删不掉");
    expect(text).toContain("请先删除这些实例");
  });

  it("没有实例在用时直接说明", () => {
    expect(cloudAccountDeleteText(0)).toBe("所有空间里都没有执行器实例在用这个云账户。");
  });

  it("没查到实例数时写一般影响", () => {
    expect(cloudAccountDeleteText(null)).toContain("请先确认没有实例在用");
  });
});
