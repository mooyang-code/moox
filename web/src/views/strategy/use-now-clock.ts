import { onMounted, onUnmounted, ref } from "vue";
import { serverNow } from "@/api/admin/http";

// 比较服务端给出的时间（目标有效期等）要用服务端时钟：浏览器时钟可能不准；还没读到服务端时间时用浏览器时钟。
const now = () => serverNow() ?? Date.now();

/**
 * 响应式的当前时间（毫秒，按服务端时钟）：每隔 intervalMs 刷新一次，组件卸载时停止。
 * 依赖 Date.now() 的 computed 没有响应式依赖，不会自己重算，需要读取这个时钟才会在时间流逝后更新（例如目标过期）。
 */
export function useNowClock(intervalMs = 1000) {
  const nowMs = ref(now());
  let timer: ReturnType<typeof setInterval> | undefined;
  onMounted(() => {
    nowMs.value = now();
    timer = setInterval(() => (nowMs.value = now()), intervalMs);
  });
  onUnmounted(() => {
    if (timer !== undefined) clearInterval(timer);
    timer = undefined;
  });
  return nowMs;
}
