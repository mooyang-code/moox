import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
/**
 * 全局配置
 * @methods setCollapsed 设置菜单折叠
 * @methods setRefreshPage 刷新页面
 */
const themeConfig = () => {
  // 是否折叠菜单
  const collapsed = ref<boolean>(false);
  // 刷新页面
  const refreshPage = ref<boolean>(true);
  // 黑暗模式
  const darkMode = ref<boolean>(false);

  // 折叠菜单
  function setCollapsed(data: boolean) {
    collapsed.value = data;
  }
  // 刷新页面
  function setRefreshPage(data: boolean) {
    refreshPage.value = data;
  }

  return {
    collapsed,
    refreshPage,
    darkMode,
    setCollapsed,
    setRefreshPage
  };
};

// 只持久化布局偏好：refreshPage 是刷新当前页时的临时开关，存下来的 false 会让主区域在重载后一直空白
export const useThemeConfig = defineStore("theme-config", themeConfig, {
  persist: persistedstateConfig("theme-config", ["collapsed", "darkMode"])
});
