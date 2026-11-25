import NProgress from "@/config/nprogress";
import { loginLocation } from "@/utils/safe-redirect";
import pinia from "@/store/index";
import { createRouter, createWebHashHistory } from "vue-router";
import { staticRoutes, notFoundRoutes } from "@/router/route.ts";
import { currentlyRoute } from "@/router/route-output";
import { storeToRefs } from "pinia";
import { useUserInfoStore } from "@/store/modules/user-info";
import { isEmptyObject } from "@/utils/index";

// 路由全部来自 route.ts 的路由表：登录页、layout 下的全部页面、404 兜底。
const router = createRouter({
  history: createWebHashHistory(),
  routes: [...staticRoutes, ...notFoundRoutes]
});

/**
 * 路由加载前判断用户是否登录。会话在 HttpOnly Cookie 里，页面读不到，是否登录以 GetUserInfo 的结果为准：
 * 1、去登录页：已有账号信息就去首页，否则放行
 * 2、去别的页面且还没有账号信息（刚打开或刷新）：请求一次 GetUserInfo；失败就带着原地址去登录页。
 *    失败时不调退出：可能只是限流或网络抖动，不能因此注销一个有效会话
 * 3、已有账号信息：照常放行（会话过期时接口返回 401，请求层会清掉账号并回登录页）
 */
router.beforeEach(async (to: any, _from: any, next: any) => {
  NProgress.start(); // 开启进度条
  const store = useUserInfoStore(pinia);
  const { account } = storeToRefs(store);

  // next()内部加了path等于跳转指定路由会再次触发router.beforeEach，内部无参数等于放行，不会触发router.beforeEach
  if (to.path === "/login") {
    if (isEmptyObject(account.value.user)) {
      next();
      return;
    }
    next("/home");
    return;
  }

  // 刚打开或刷新：先取账号信息
  if (isEmptyObject(account.value.user)) {
    try {
      await store.setAccount();
    } catch (error: any) {
      console.error("路由守卫: 获取用户信息失败", error);
      store.clearAccount();
      // 跳转到登录页，带着原地址，登录后回来
      next(loginLocation(to.fullPath));
      return;
    }
  }

  next();
  // 项目内的跳转，处理跳转路由高亮
  currentlyRoute(to);
});

// 路由跳转错误
router.onError((error: any) => {
  NProgress.done();
  console.warn("路由错误", error.message);
});

// 路由加载后
router.afterEach(() => {
  NProgress.done();
});

export default router;
