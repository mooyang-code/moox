import { onBeforeUnmount, onMounted } from "vue";
import { onBeforeRouteLeave, onBeforeRouteUpdate } from "vue-router";

/**
 * 编辑器的未保存提示：站内离开（换页、换编辑对象）时确认；刷新、关标签页时由浏览器弹出自己的确认框。
 * dirty 为真时才拦，保存后（dirty 变假）自动放行。
 */
export function useUnsavedGuard(dirty: () => boolean, messages: { leave: string; update?: string }) {
  onBeforeRouteLeave(() => !dirty() || window.confirm(messages.leave));
  onBeforeRouteUpdate(() => !dirty() || window.confirm(messages.update ?? messages.leave));

  function onBeforeUnload(event: BeforeUnloadEvent) {
    if (!dirty()) return;
    event.preventDefault();
    // 旧版浏览器要设置 returnValue 才会弹出确认框
    event.returnValue = "";
  }
  onMounted(() => window.addEventListener("beforeunload", onBeforeUnload));
  onBeforeUnmount(() => window.removeEventListener("beforeunload", onBeforeUnload));
}
