import pinia from "@/store/index";
import { storeToRefs } from "pinia";
import { useRoutesConfigStore } from "@/store/modules/route-config";

/**
 * 标签栏用到的路由处理方法
 * @returns 路由方法
 */
export const useRoutingMethod = () => {
  /**
   * 从tabs路由中查找路由
   * @param {string} key 路由的name
   * @returns 查找到的路由，undefined则表示未找到
   */
  const findTagsList = (key: string) => {
    const routerStore = useRoutesConfigStore(pinia);
    const { tabsList } = storeToRefs(routerStore);
    return tabsList.value.find((item: Menu.MenuOptions) => item.name == key);
  };

  /**
   * 检测是否是动态匹配路由，如果是动态匹配路由，则path必然带有"/:"字样，例如：/user/:id
   * @param {string} path 路由path
   * @returns 是否是动态匹配路由
   */
  const isDynamicRoute = (path: string) => {
    return path.includes("/:");
  };

  return {
    findTagsList,
    isDynamicRoute
  };
};
