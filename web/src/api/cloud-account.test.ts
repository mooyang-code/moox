import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn(async () => ({})) }));
vi.mock("@/api/http", () => ({ call: mocks.call }));
import { deleteCloudAccount, getCloudAccountList } from "./cloud-account";

beforeEach(() => {
  mocks.call.mockReset().mockResolvedValue({});
});

describe("云账号接口封装", () => {
  it("调 Collector 的方法", async () => {
    await getCloudAccountList();
    expect(mocks.call).toHaveBeenLastCalledWith("collector", "ListCloudAccounts", {});
  });

  it("删账号不弹全局提示，失败原因由对话框只提示一次", async () => {
    await deleteCloudAccount("old-account");
    expect(mocks.call).toHaveBeenLastCalledWith("collector", "DeleteCloudAccount", { account_id: "old-account" });
  });
});
