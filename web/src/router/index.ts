import NProgress from "@/config/nprogress";
import pinia from "@/store/index";
import { createRouter, createWebHashHistory } from "vue-router";
import { staticRoutes, notFoundAndNoPower } from "@/router/route.ts";
import { currentlyRoute } from "@/router/route-output";
import { storeToRefs } from "pinia";
import { useUserInfoStore } from "@/store/modules/user-info";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { useLoadingStore } from "@/store/modules/loading";
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
   * 1、staticRoutes登录页、layout页、默认页面('/')
   * 2、notFoundAndNoPower 添加默认 401、500界面，防止提示 No match found for location with path 'xxx'
   * 2、后端控制路由中也需要添加 notFoundAndNoPower 401、500界面
   * 静态添加 notFoundAndNoPower 401、500界面将全屏显示
   * 如果要 notFoundAndNoPower 在layout容器展示，则需要移除静态添加并将其添加到缓存路由树
   */
  routes: [...staticRoutes, ...notFoundAndNoPower]
});

/**
 * 路由加载前需要判断用户是否登录
 * 1、去登录页，无token，放行
 * 2、没有token，直接重定向到登录页
 * 3、去登录页，有token，直接重定向到home页
 * 4、去非登录页，有token，用户信息是否存在，有则放行，否则重新获取路由信息、初始化路由
 * 注意：
 * 全局routeTree不能持久化缓存
 * 页面刷新会导致addRoute动态添加的路由失效，需要重新初始化路由
 */
router.beforeEach(async (to: any, from: any, next: any) => {
  NProgress.start(); // 开启进度条
  const store = useUserInfoStore(pinia);
  const loadingStore = useLoadingStore(pinia);
  const routeStore = useRoutesConfigStore(pinia);
  const { token, account } = storeToRefs(store);

  // 显示路由加载状态
  loadingStore.showRouteLoading();

  // 如果从新建项目页面切换到其他页面，清理该组件的缓存
  if (from.name === 'create-project' && to.name !== 'create-project') {
    routeStore.removeRouteName('CreateProject');
    console.log('清理新建项目组件缓存');
    
    // 强制清理可能残留的DOM元素
    setTimeout(() => {
      const elementsToRemove = document.querySelectorAll('[data-v-152e326b]');
      elementsToRemove.forEach(element => {
        console.log('强制清理残留DOM元素:', element);
        element.remove();
      });
    }, 100);
  }
  
  // 如果从步骤表单页面切换到其他页面，清理该组件的缓存
  if (from.name === 'step-form' && to.name !== 'step-form') {
    routeStore.removeRouteName('StepForm');
    console.log('清理步骤表单组件缓存');
  }
  // console.log("去", to, "来自", from);
  // next()内部加了path等于跳转指定路由会再次触发router.beforeEach，内部无参数等于放行，不会触发router.beforeEach
  if (to.path === "/login" && !token.value) {
    // 1、去登录页，无token，放行
    next();
  } else if (!token.value) {
    // 2、没有token，直接重定向到登录页
    next("/login");
  } else if (to.path === "/login" && token.value) {
    // 3、去登录页，有token，检查token是否有效
    // 如果用户信息为空，说明token可能无效，允许进入登录页
    if (isEmptyObject(account.value.user)) {
      console.log("路由守卫: 虽然有token但用户信息为空，允许进入登录页");
      next();
      return;
    }
    // 有token且有用户信息，直接重定向到home页
    next("/home");
    // 项目内的跳转，处理跳转路由高亮
    currentlyRoute(to);
  } else {
    // 4、去非登录页，有token，用户信息是否存在，有则放行，否则重新获取路由信息、初始化路由
    const routeStore = useRoutesConfigStore(pinia);

    // 判断账号信息是否获取，先获取账号信息和路由信息，添加路由后再跳转(页面刷新时触发)
    // 解决刷新页面404的问题
    if (isEmptyObject(account.value.user)) {
      try {
        // 获取账号信息
        await store.setAccount();
        // 获取路由信息
        await routeStore.initSetRouter();
        // 判断是否是动态路由
        const { isDynamicRoute } = useRoutingMethod();
        if (isDynamicRoute(to.path)) {
          next({ name: to.name, params: to.params, replace: true });
        } else {
          next({ path: to.path, query: to.query, replace: true });
        }
      } catch (error: any) {
        console.error("路由守卫: 获取用户信息失败", error);
        
        // 确保完全清除用户状态，避免死循环
        await store.logOut();
        
        // 直接跳转到登录页，不做任何其他检查
        console.log("路由守卫: 用户认证失败，强制跳转登录页");
        next('/login');
        return;
      }
    } else {
      // 检查路由是否存在，如果不存在则重新初始化路由
      if (!router.hasRoute(to.name as string) && to.name !== 'not-found') {
        try {
          console.log("路由不存在，重新初始化路由:", to.name);
          await routeStore.initSetRouter();
          // 使用setTimeout确保路由已经添加完成
          setTimeout(() => {
            next({ ...to, replace: true });
          }, 100);
          return;
        } catch (error) {
          console.error("路由重新初始化失败:", error);
          // 如果路由初始化失败，跳转到首页
          next('/home');
          return;
        }
      }

      // 获取外链路由的处理函数
      // 所有的路由正常放行，只不过额外判断是否是外链，如果是，则打开新窗口跳转外链
      // 外链的页面依旧正常打开，只不过不会参与缓存与tabs显示，符合路由跳转的直觉
      const { openExternalLinks } = useRoutingMethod();
      // 处理外链跳转
      openExternalLinks(to);
      // 动态路由添加过走这里，直接放行
      next();
      // 项目内的跳转，处理跳转路由高亮
      currentlyRoute(to);
    }
  }
});

// 路由跳转错误
router.onError((error: any) => {
  NProgress.done();
  const loadingStore = useLoadingStore(pinia);
  loadingStore.hideRouteLoading();
  console.warn("路由错误", error.message);
});

// 路由加载后
router.afterEach(() => {
  NProgress.done();
  const loadingStore = useLoadingStore(pinia);
  loadingStore.hideRouteLoading();
});

export default router;
