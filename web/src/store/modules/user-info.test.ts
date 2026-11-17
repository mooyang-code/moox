import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { getUserInfoAPI, logoutAPI } from "@/api/modules/user/index";
import { expireBrowserSession } from "@/api/admin/space-client";
import { useUserInfoStore } from "./user-info";

vi.mock("@/api/modules/user/index", () => ({
  getUserInfoAPI: vi.fn(),
  logoutAPI: vi.fn()
}));

describe("user-info store", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.mocked(getUserInfoAPI).mockReset();
    vi.mocked(logoutAPI).mockReset();
    localStorage.clear();
  });

  // 系统只有一个超级用户：用户信息里没有 role 也要拿到完整账号，不再被当成游客。
  it("loads the account from GetUserInfo and keeps no token", async () => {
    const store = useUserInfoStore();
    vi.mocked(getUserInfoAPI).mockResolvedValue({
      user_info: { user_id: "u1", username: "admin", last_login_ip: "1.2.3.4", last_login_at: 1700000000, created_at: 1690000000 }
    } as never);

    await store.setAccount();

    expect(getUserInfoAPI).toHaveBeenCalledTimes(1);
    expect(store.account.user).toMatchObject({ id: "u1", userName: "admin", loginIp: "1.2.3.4" });
    expect(store.account).not.toHaveProperty("roles");
    expect(store).not.toHaveProperty("token");
    expect(store).not.toHaveProperty("sessionId");
    expect(localStorage.length).toBe(0);
  });

  it("throws without touching the account when GetUserInfo fails", async () => {
    const store = useUserInfoStore();
    vi.mocked(getUserInfoAPI).mockRejectedValue(new Error("401"));
    await expect(store.setAccount()).rejects.toThrow("401");
    expect(store.account.user).toEqual({});
    expect(logoutAPI).not.toHaveBeenCalled();
  });

  it("logs out on the server and clears the account even when the server is unreachable", async () => {
    const store = useUserInfoStore();
    store.account = { user: { id: "u1", userName: "admin" } };
    vi.mocked(logoutAPI).mockRejectedValue(new Error("network"));
    await store.logOut();
    expect(logoutAPI).toHaveBeenCalledTimes(1);
    expect(store.account.user).toEqual({});
  });

  // 任何接口 401 时清掉账号，路由守卫才不会把人从登录页推回首页。
  it("clears the account when any request reports an expired session", () => {
    const store = useUserInfoStore();
    store.account = { user: { id: "u1", userName: "admin" } };
    expireBrowserSession();
    expect(store.account.user).toEqual({});
    window.location.hash = "#/";
  });
});
