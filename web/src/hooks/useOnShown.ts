import { onActivated, onMounted } from "vue";

/**
 * 页面每次显示时执行：挂载时一次，之后在 keep-alive 里每次切回再执行一次。
 * keep-alive 首次挂载会先触发 mounted 再触发 activated，这里跳过紧跟着的那次 activated，不重复执行；
 * 不在 keep-alive 里（例如单测直接挂载）时只在挂载时执行。
 */
export function onShown(callback: () => void) {
  let justMounted = false;
  onMounted(() => {
    justMounted = true;
    callback();
  });
  onActivated(() => {
    if (justMounted) {
      justMounted = false;
      return;
    }
    callback();
  });
}
