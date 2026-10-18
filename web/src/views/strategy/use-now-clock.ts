import { onMounted, onUnmounted, ref } from "vue";

/**
 * 响应式的当前时间（毫秒）：每隔 intervalMs 刷新一次，组件卸载时停止。
 * 依赖 Date.now() 的 computed 没有响应式依赖，不会自己重算，需要读取这个时钟才会在时间流逝后更新（例如目标过期）。
 */
export function useNowClock(intervalMs = 1000) {
  const nowMs = ref(Date.now());
  let timer: ReturnType<typeof setInterval> | undefined;
  onMounted(() => {
    nowMs.value = Date.now();
    timer = setInterval(() => (nowMs.value = Date.now()), intervalMs);
  });
  onUnmounted(() => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
  });
  return nowMs;
}
