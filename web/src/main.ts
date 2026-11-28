import { installStaleSpaceToastFilter } from "@/api/admin/stale-space";
import { RequestError, reportError } from "@/api/http";
import { installMessageDedupe } from "@/utils/message-dedupe";
import { createApp } from "vue";
import "@/style.css";
import App from "@/App.vue";
// arco-design 函数式组件上下文绑定（模板 <a-*> 组件由 unplugin-vue-components 按需解析）
import { Message, Modal, Notification } from "@arco-design/web-vue";
// vue-router
import router from "@/router/index";
// pinia
import pinia from "@/store/index";
// arco-css
import "@arco-design/web-vue/dist/arco.css";
// 额外引入图标库
import ArcoVueIcon from "@arco-design/web-vue/es/icon";
// 注册全局svg
import "virtual:svg-icons-register";
// 引入字体
import "@/assets/fonts/fonts.scss";

// vchart黑暗模式 - 延迟初始化，避免阻塞主线程
let vchartThemeInitialized = false;
async function initVChartTheme() {
  if (vchartThemeInitialized) return;
  try {
    const { initVChartArcoTheme } = await import("@visactor/vchart-arco-theme");
    initVChartArcoTheme();
    vchartThemeInitialized = true;
  } catch (error) {
    console.warn("VChart theme initialization failed:", error);
  }
}

// 在路由准备好后初始化图表主题
router.isReady().then(initVChartTheme);

installStaleSpaceToastFilter();
installMessageDedupe();
const app = createApp(App);

// 请求层不弹提示，页面自己处理失败；页面没有处理、一路抛到这里的请求失败在这里提示一次，不会无声无息。
function reportUnhandledRequest(error: unknown): boolean {
  if (!(error instanceof RequestError)) return false;
  reportError(error);
  return true;
}
window.addEventListener("unhandledrejection", event => {
  if (reportUnhandledRequest(event.reason)) event.preventDefault();
});

// 全局错误处理
app.config.errorHandler = (err, instance, info) => {
  if (reportUnhandledRequest(err)) return;
  console.error("全局错误捕获:", err);
  console.error("错误实例:", instance);
  console.error("错误信息:", info);

  // 如果是resetFields相关错误，给出更友好的提示
  if (err instanceof Error && err.message && err.message.includes("resetFields")) {
    console.warn("表单重置方法调用失败，这通常是因为组件还未完全挂载");
  }
};

Message._context = app._context;
Modal._context = app._context;
Notification._context = app._context;

app.use(pinia);
app.use(ArcoVueIcon);
app.use(router);

// 挂载应用
app.mount("#app");
