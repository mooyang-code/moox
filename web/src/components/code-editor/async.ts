import { defineAsyncComponent } from "vue";

// 消费者只使用这个异步入口，保证 CodeMirror 单独成 chunk、不进入首屏。
export const AsyncCodeEditor = defineAsyncComponent(() => import("./index.vue"));
