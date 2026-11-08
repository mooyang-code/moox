import { createI18n } from "vue-i18n";
import zhCN from "@/lang/modules/zhCN";

// 界面只有中文：菜单、标签栏和面包屑的标题从中文表里取
const i18n = createI18n({
  legacy: false, // Composition API模式需要设为false
  globalInjection: true, // 全局生效: $
  locale: "zh-CN",
  messages: { "zh-CN": zhCN },
  missing: (_: string, key: string) => {
    return removeBeforeFirstDot(key);
  }
});

/**
 * @param { string } str 国际化key
 * @returns 去掉第一个点之前的字符串，例如："menu.home" => "home"，否则返回原字符串
 */
function removeBeforeFirstDot(str: string) {
  const dotIndex = str.indexOf(".");
  return dotIndex >= 0 ? str.slice(dotIndex + 1) : str;
}

export default i18n;
