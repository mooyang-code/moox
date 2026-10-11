import { beforeEach, describe, expect, it, vi } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { getUserInfoAPI } from "@/api/modules/user/index";
import { clearBrowserSession } from "@/api/admin/signed-client";
import { useUserInfoStore } from "./user-info";

vi.mock("@/api/modules/user/index", () => ({
  getUserInfoAPI: vi.fn(),
  logoutAPI: vi.fn()
}));
vi.mock("@/api/admin/signed-client", () => ({
  clearBrowserSession: vi.fn(async () => undefined)
}));
vi.mock("@/utils/request-signing", () => ({
  loadSigningSession: vi.fn(),
  saveSigningSession: vi.fn()
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("user-info store setAccount", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.mocked(getUserInfoAPI).mockReset();
    vi.mocked(clearBrowserSession).mockClear();
  });

  it("a late failure of an old session does not clear the new login", async () => {
    const store = useUserInfoStore();
    store.token = "token-1";
    store.sessionId = "session-1";
    const pending = deferred<never>();
    vi.mocked(getUserInfoAPI).mockReturnValue(pending.promise as never);

    const request = store.setAccount();
    store.token = "token-2"; // 请求期间重新登录
    store.sessionId = "session-2";
    pending.reject(new Error("401"));

    await expect(request).rejects.toMatchObject({ name: "StaleAccountRequestError" });
    expect(store.token).toBe("token-2");
    expect(store.sessionId).toBe("session-2");
    expect(clearBrowserSession).not.toHaveBeenCalled();
  });

  it("a late success of an old session does not overwrite the new profile", async () => {
    const store = useUserInfoStore();
    store.token = "token-1";
    store.sessionId = "session-1";
    const pending = deferred<unknown>();
    vi.mocked(getUserInfoAPI).mockReturnValue(pending.promise as never);

    const request = store.setAccount();
    store.token = "token-2";
    store.sessionId = "session-2";
    pending.resolve({ user_info: { user_id: "old-user", username: "old", role: 2 } });

    await expect(request).rejects.toMatchObject({ name: "StaleAccountRequestError" });
    expect(store.account.user).toEqual({});
  });

  it("still clears the session when the current session's request fails", async () => {
    const store = useUserInfoStore();
    store.token = "token-1";
    store.sessionId = "session-1";
    vi.mocked(getUserInfoAPI).mockRejectedValue(new Error("401"));

    await expect(store.setAccount()).rejects.toMatchObject({ name: "AuthenticationError" });
    expect(store.token).toBe("");
    expect(clearBrowserSession).toHaveBeenCalled();
  });
});
