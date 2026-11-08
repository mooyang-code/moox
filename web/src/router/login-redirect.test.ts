import { beforeEach, describe, expect, it, vi } from "vitest";

// 守卫依赖的 store 换成可控的假实现：会话、用户信息由测试决定
const session = vi.hoisted(() => ({ accountFails: false }));
vi.mock("@/config/nprogress", () => ({ default: { start: vi.fn(), done: vi.fn() } }));
vi.mock("@/router/route-output", () => ({ currentlyRoute: vi.fn() }));
vi.mock("@/store/modules/route-config", () => ({
  useRoutesConfigStore: () => ({ initSetRouter: vi.fn(async () => undefined) })
}));
vi.mock("@/store/modules/user-info", async () => {
  const { defineStore } = await import("pinia");
  const { ref } = await import("vue");
  return {
    useUserInfoStore: defineStore("user-info-test", () => {
      const token = ref("");
      const account = ref<{ user: Record<string, unknown> }>({ user: {} });
      return {
        token,
        account,
        hasValidSession: async () => true,
        logOut: async () => {
          token.value = "";
        },
        setAccount: async () => {
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
  const store = useUserInfoStore(pinia);
  store.token = "";
  store.account = { user: {} };
  await router.push("/login");
});

describe("路由守卫跳登录页时带上原地址", () => {
  it("没有会话时去别的页面：跳登录页并带上原地址（含查询参数）", async () => {
    await router.push("/strategy/overview?instance=a");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBe("/strategy/overview?instance=a");
  });

  it("有会话但取用户信息失败：登出后跳登录页，同样带上原地址", async () => {
    useUserInfoStore(pinia).token = "t";
    session.accountFails = true;
    await router.push("/trading/accounts");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBe("/trading/accounts");
    expect(useUserInfoStore(pinia).token).toBe("");
  });

  it("去首页时不带 redirect", async () => {
    await router.push("/home");
    expect(router.currentRoute.value.path).toBe("/login");
    expect(router.currentRoute.value.query.redirect).toBeUndefined();
  });
});
