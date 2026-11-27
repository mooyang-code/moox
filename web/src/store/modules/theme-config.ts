import { ref } from "vue";
import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
/**
 * 全局配置
 * @methods setCollapsed 设置桌面端侧栏折叠偏好（持久化）
 * @methods setDrawerVisible 设置移动端临时抽屉显隐（不持久化）
 * @methods setRefreshPage 刷新页面
 */
const themeConfig = () => {
  // 桌面端侧栏是否折叠（仅桌面偏好，持久化到 localStorage）
  const collapsed = ref<boolean>(false);
  // 移动端/窄屏临时导航抽屉显隐（不持久化，避免污染桌面端偏好）
  const drawerVisible = ref<boolean>(false);
  // 刷新页面
  const refreshPage = ref<boolean>(true);
  // 黑暗模式
  const darkMode = ref<boolean>(false);

  // 设置桌面端侧栏折叠偏好
  function setCollapsed(data: boolean) {
    collapsed.value = data;
  }
  // 设置移动端抽屉显隐
  function setDrawerVisible(data: boolean) {
    drawerVisible.value = data;
  }
  // 刷新页面
  function setRefreshPage(data: boolean) {
    refreshPage.value = data;
  }

  return {
    collapsed,
    drawerVisible,
    refreshPage,
    darkMode,
    setCollapsed,
    setDrawerVisible,
    setRefreshPage
  };
};

// 只持久化桌面布局偏好：drawerVisible 与 refreshPage 是临时交互开关，不写入 localStorage
export const useThemeConfig = defineStore("theme-config", themeConfig, {
  persist: persistedstateConfig("theme-config", ["collapsed", "darkMode"])
});
