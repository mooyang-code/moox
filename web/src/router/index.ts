import NProgress from "@/config/nprogress";
import { loginLocation } from "@/utils/safe-redirect";
import pinia from "@/store/index";
import { createRouter, createWebHashHistory } from "vue-router";
import { staticRoutes, notFoundRoutes } from "@/router/route.ts";
import { currentlyRoute } from "@/router/route-output";
import { storeToRefs } from "pinia";
import { useUserInfoStore } from "@/store/modules/user-info";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useRoutingMethod } from "@/hooks/useRoutingMethod";
import { isEmptyObject } from "@/utils/index";
/**
 * 创建vue的路由示例
 * @method createRouter(options: RouterOptions): Router
 * @link 参考：https://next.router.vuejs.org/zh/api/#createrouter
 */
const router = createRouter({
  history: createWebHashHistory(),
  /**
   * 设置静态路由，其它的路由通过addRoute动态添加
   * 1、staticRoutes：登录页、layout页、默认页面('/')
   * 2、notFoundRoutes：404 兜底，防止提示 No match found for location with path 'xxx'；静态添加时全屏显示
   */
  routes: [...staticRoutes, ...notFoundRoutes]
});

/**
 * 路由加载前判断用户是否登录。会话在 HttpOnly Cookie 里，页面读不到，是否登录以 GetUserInfo 的结果为准：
 * 1、去登录页：已有账号信息就去首页，否则放行
 * 2、去别的页面且还没有账号信息（刚打开或刷新）：请求一次 GetUserInfo 并初始化路由；失败就带着原地址去登录页。
 *    失败时不调退出：可能只是限流或网络抖动，不能因此注销一个有效会话
 * 3、已有账号信息：照常放行（会话过期时接口返回 401，请求层会清掉账号并回登录页）
 * 注意：
 * 全局routeTree不能持久化缓存
 * 页面刷新会导致addRoute动态添加的路由失效，需要重新初始化路由
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
    // 项目内的跳转，处理跳转路由高亮
    currentlyRoute(to);
    return;
  }

  const routeStore = useRoutesConfigStore(pinia);
  // 判断账号信息是否获取，先获取账号信息和路由信息，添加路由后再跳转(页面刷新时触发)
  // 解决刷新页面404的问题
  if (isEmptyObject(account.value.user)) {
    try {
      await store.setAccount();
      await routeStore.initSetRouter();
    } catch (error: any) {
      console.error("路由守卫: 获取用户信息失败", error);
      store.clearAccount();
      // 跳转到登录页，带着原地址，登录后回来
      next(loginLocation(to.fullPath));
      return;
    }
    // 判断是否是动态路由
    const { isDynamicRoute } = useRoutingMethod();
    if (isDynamicRoute(to.path)) {
      next({ name: to.name, params: to.params, replace: true });
    } else {
      next({ path: to.path, query: to.query, replace: true });
    }
    return;
  }

  // 检查路由是否存在，如果不存在则重新初始化路由
  if (!router.hasRoute(to.name as string) && to.name !== "not-found") {
    try {
      await routeStore.initSetRouter();
      // 使用setTimeout确保路由已经添加完成
      setTimeout(() => {
        next({ ...to, replace: true });
      }, 100);
      return;
    } catch (error) {
      console.error("路由重新初始化失败:", error);
      // 如果路由初始化失败，跳转到首页
      next("/home");
      return;
    }
  }

  // 获取外链路由的处理函数
  // 所有的路由正常放行，只不过额外判断是否是外链，如果是，则打开新窗口跳转外链
  // 外链的页面依旧正常打开，只不过不进标签栏，符合路由跳转的直觉
  const { openExternalLinks } = useRoutingMethod();
  // 处理外链跳转
  openExternalLinks(to);
  // 动态路由添加过走这里，直接放行
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
