import { ref } from "vue";
import { defineStore } from "pinia";

/**
 * 标签页与当前路由（菜单直接读路由表，见 router/route.ts）
 * @methods setTabs 添加tabs标签页
 * @methods setCurrentRoute 设置系统内的当前路由
 * @methods removeTabsList 删除tabs页的指定路由
 * @methods clearTabs 退出登录时清空标签页
 */
export const routesConfigStore = () => {
  const tabsList = ref<any>([]); // 标签页数据
  const currentRoute = ref<any>({}); // 当前路由

  /**
   * 添加tabs标签页
   * @param {object} data 当前tabs路由
   */
  function setTabs(data: Menu.MenuOptions) {
    // 当前路由在tags中是否存在
    let index = tabsList.value.findIndex((item: Menu.MenuOptions) => item.name === data.name);
    // 如果存在，新路由替换旧路由，因为路由存在并不代表本次跳转的参数一致
    if (index != -1) return tabsList.value.splice(index, 1, data);
    // 不存在，直接缓存
    tabsList.value.push(data);
  }
  /**
   * 设置系统内的当前路由数据
   * @param {object} data 当前路由
   */
  function setCurrentRoute(data: Menu.MenuOptions) {
    // 名称一样不代表参数相同，这不用做已存在匹配，直接存储当前路由
    currentRoute.value = data;
  }
  /**
   * 删除tabs页的指定路由；固定页或不存在时不删除并返回 false
   * @param {string} key 路由name
   */
  function removeTabsList(key: string): boolean {
    const index = tabsList.value.findIndex((item: Menu.MenuOptions) => item.name === key);
    if (index === -1 || tabsList.value[index].meta?.affix) return false;
    tabsList.value.splice(index, 1);
    return true;
  }
  /** 退出登录时清空标签页 */
  function clearTabs() {
    tabsList.value = [];
  }

  return {
    tabsList,
    currentRoute,
    setTabs,
    setCurrentRoute,
    removeTabsList,
    clearTabs
  };
};

export const useRoutesConfigStore = defineStore("route-config", routesConfigStore);
