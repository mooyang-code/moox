import { beforeEach, describe, expect, it, vi } from "vitest";

// 守卫依赖的 store 换成可控的假实现：GetUserInfo 成功与否由测试决定
const session = vi.hoisted(() => ({ accountFails: false, calls: 0, logouts: 0 }));
vi.mock("@/config/nprogress", () => ({ default: { start: vi.fn(), done: vi.fn() } }));
vi.mock("@/router/route-output", () => ({ currentlyRoute: vi.fn() }));
vi.mock("@/store/modules/user-info", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useUserInfoStore: defineStore("user-info-test", () => {
      const account = ref<{ user: Record<string, unknown> }>({ user: {} });
      return {
        account,
        clearAccount: () => {
          account.value = { user: {} };
        },
        logOut: async () => {
          session.logouts += 1;
        },
        setAccount: async () => {
          session.calls += 1;
          if (session.accountFails) throw new Error("会话已失效");
          account.value = { user: { name: "admin" } };
        }
      };
    })
  };
});
import pinia from "@/store/index";
import { useUserInfoStore } from "@/store/modules/user-info";
import router from "./index";

vi.spyOn(console, "error").mockImplementation(() => undefined);

beforeEach(async () => {
  session.accountFails = false;
  session.calls = 0;
  session.logouts = 0;
  const store = useUserInfoStore(pinia);
  store.account = { user: {} };
  await router.push("/login");
});

describe("路由守卫按 GetUserInfo 判断登录", () => {
  it("没有会话时去别的页面：请求一次 GetUserInfo，失败后跳登录页并带上原地址（含查询参数）", async () => {
    session.accountFails = true;
    await router.push("/strategy/overview?instance=a");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBe("/strategy/overview?instance=a");
    expect(session.calls).toBe(1);
    expect(session.logouts).toBe(0);
  });

  it("取用户信息失败（例如限流）不调退出，免得注销一个有效会话", async () => {
    session.accountFails = true;
    await router.push("/trading/accounts");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBe("/trading/accounts");
    expect(session.logouts).toBe(0);
  });

  it("去首页且没有会话时不带 redirect", async () => {
    session.accountFails = true;
    await router.push("/home");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBeUndefined();
  });

  it("Cookie 有效时 GetUserInfo 成功就放行，之后不再重复请求", async () => {
    await router.push("/home");
    expect(router.currentRoute.value.path).not.toBe("/login");
    expect(session.calls).toBe(1);
    await router.push("/login");
    // 已登录时去登录页回首页
    expect(router.currentRoute.value.path).toBe("/home");
    expect(session.calls).toBe(1);
  });
});
