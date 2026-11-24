import pinia from "@/store/index";
import { storeToRefs } from "pinia";
import { useRoutesConfigStore } from "@/store/modules/route-config";
import { deepClone } from "@/utils/index";
import { flatRoutes } from "@/router/route";

/**
 * 统一处理所有的路由跳转：当前路由高亮、tabs栏数据。
 * menu和tabs以及手动刷新浏览器等功能只需要跳转即可，高亮和标签栏的逻辑这边负责。
 * 只处理路由表里不隐藏的页面：登录页、404、编辑页与详情页不进标签栏。
 * @param {any} current 需要跳转的路由和路由参数
 */
export const currentlyRoute = (current: any) => {
  const routes = flatRoutes();
  const entry = routes.find(item => item.name === current.name);
  if (!entry || entry.meta.hide) return;

  const route = deepCloneRoute(current);
  const store = useRoutesConfigStore(pinia);
  const { tabsList } = storeToRefs(store);
  // tabs无数据则默认添加首页
  if (tabsList.value.length == 0) {
    const home = routes[0];
    store.setTabs(deepCloneRoute({ ...home, query: {}, params: {} }));
  }

  // 存入当前路由-高亮
  store.setCurrentRoute(route);

  // 存入tabs栏数据
  store.setTabs(route);
};

/**
 * 深拷贝路由，切断与原路由的联系，防止路由参数污染
 * @param route 当前路由
 * @returns 深拷贝后的路由
 */
export const deepCloneRoute = (route: any) => {
  return deepClone({
    path: route.path,
    name: route.name,
    meta: route.meta,
    query: route.query,
    params: route.params
  });
};
