import { createApp } from "vue";
import "@/style.css";
import App from "@/App.vue";
// arco-design
import ArcoVue from "@arco-design/web-vue";
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
// 引入i18n
import i18n from "@/lang/index";
// 引入字体
import "@/assets/fonts/fonts.scss";
// 引入自定义指令
import directives from "@/directives/index";
// 动态加载工具
import { preloadResource, prefetchResource, setupLazyImages } from "@/utils/dynamic-loader";

// 预加载关键资源
preloadResource('https://unpkg.com/@visactor/vchart@latest/build/index.min.js');
prefetchResource('https://unpkg.com/@wangeditor/editor@latest/dist/index.min.js');
prefetchResource('https://unpkg.com/xgplayer@latest/dist/index.min.js');

// vchart黑暗模式 - 延迟初始化，避免阻塞主线程
let vchartThemeInitialized = false;
async function initVChartTheme() {
  if (vchartThemeInitialized) return;
  try {
    const { initVChartArcoTheme } = await import("@visactor/vchart-arco-theme");
    initVChartArcoTheme();
    vchartThemeInitialized = true;
  } catch (error) {
    console.warn('VChart theme initialization failed:', error);
  }
}

// 在路由准备好后初始化图表主题
router.isReady().then(initVChartTheme);

const app = createApp(App);

// 全局错误处理
app.config.errorHandler = (err, instance, info) => {
  console.error('全局错误捕获:', err);
  console.error('错误实例:', instance);
  console.error('错误信息:', info);

  // 如果是resetFields相关错误，给出更友好的提示
  if (err instanceof Error && err.message && err.message.includes('resetFields')) {
    console.warn('表单重置方法调用失败，这通常是因为组件还未完全挂载');
  }
};

// app.use(plugin, options)
// 其中 plugin 表示要传递的插件对象， options 参数是可选的，表示选项配置
// https://cn.vuejs.org/api/application.html#app-use

app.use(ArcoVue, {
  componentPrefix: "arco"
});
app.use(pinia);
app.use(ArcoVueIcon);
app.use(router);
app.use(i18n);
app.use(directives);

// 挂载应用
app.mount("#app");

// 在应用挂载后设置懒加载
setupLazyImages();

// 使用 requestIdleCallback 在浏览器空闲时预加载非关键资源
if ('requestIdleCallback' in window) {
  requestIdleCallback(() => {
    // 预获取其他可能用到的资源
    prefetchResource('https://unpkg.com/lightweight-charts@latest/dist/lightweight-charts.standalone.production.js');
  });
}
