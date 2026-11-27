import type { Scrollbar } from "@arco-design/web-vue";

declare module "vue" {
  export interface GlobalComponents {
    // @arco-design/web-vue 导出 Scrollbar 但未写入其全局 GlobalComponents 声明
    AScrollbar: typeof Scrollbar;
  }
}
