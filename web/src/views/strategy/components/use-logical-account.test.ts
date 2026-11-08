import { flushPromises } from "@vue/test-utils";
import { ref } from "vue";
import { describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ getLogicalAccount: vi.fn() }));
vi.mock("@/api/trade", () => api);
import { useLogicalAccount } from "./use-logical-account";

describe("实例的组合账户", () => {
  it("读取失败时给出原因，不当成没有账户", async () => {
    api.getLogicalAccount.mockRejectedValueOnce(new Error("交易服务不可用"));
    const { account, error } = useLogicalAccount(ref("la-1"));
    await flushPromises();
    expect(account.value).toBeNull();
    expect(error.value).toBe("交易服务不可用");
  });

  it("读到后清掉错误；没有绑定账户时不读", async () => {
    api.getLogicalAccount.mockResolvedValueOnce({ logical_account: { logical_account_id: "la-2", name: "主组合" } });
    const id = ref<string | undefined>("la-2");
    const { account, error } = useLogicalAccount(id);
    await flushPromises();
    expect(account.value?.name).toBe("主组合");
    expect(error.value).toBe("");
    api.getLogicalAccount.mockClear();
    id.value = undefined;
    await flushPromises();
    expect(account.value).toBeNull();
    expect(api.getLogicalAccount).not.toHaveBeenCalled();
  });
});
