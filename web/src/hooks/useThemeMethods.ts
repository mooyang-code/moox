import { useThemeConfig } from "@/store/modules/theme-config";

export const useThemeMethods = () => {
  /**
   * @description: 按 darkMode 切换组件库的明暗主题（主色用组件库自带的色板，随明暗自动切换）
   */
  const setDarkMode = () => {
    if (useThemeConfig().darkMode) {
      document.body.setAttribute("arco-theme", "dark");
    } else {
      document.body.removeAttribute("arco-theme");
    }
  };

  return { setDarkMode };
};
