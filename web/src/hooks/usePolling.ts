import { selectedSpaceGeneration } from "@/api/admin/space-header";
import { onActivated, onBeforeUnmount, onDeactivated, onMounted, toValue, type MaybeRefOrGetter } from "vue";

/**
 * 在 active 为真且页面可见时按间隔执行 task；上一次未结束不会叠加，组件卸载自动停止。
 * 被 KeepAlive 缓存（deactivated）时暂停；可见性或激活恢复时立即补一次，避免切回标签页后长时间显示旧状态。
 */
export function usePolling(task: () => Promise<unknown> | unknown, intervalMs: number, active: MaybeRefOrGetter<boolean> = true) {
  let timer: number | undefined;
  let running = false;
  let paused = false;
  let runningGeneration = 0;

  async function tick() {
    // 切换空间后，旧空间的在途请求会被请求层丢弃（永不落定），其 finally 不会执行：这里不能被它永久卡住。
    if (running && runningGeneration !== selectedSpaceGeneration()) running = false;
    if (running || paused || !toValue(active) || document.hidden) return;
    running = true;
    const generation = (runningGeneration = selectedSpaceGeneration());
    try {
      await task();
    } catch {
      // 轮询失败保留上一份快照，由下一次 tick 重试。
    } finally {
      if (runningGeneration === generation) running = false;
    }
  }

  function onVisibilityChange() {
    if (!document.hidden) void tick();
  }

  onDeactivated(() => {
    paused = true;
  });
  onActivated(() => {
    paused = false;
    void tick();
  });
  onMounted(() => {
    timer = window.setInterval(() => void tick(), intervalMs);
    document.addEventListener("visibilitychange", onVisibilityChange);
  });
  onBeforeUnmount(() => {
    if (timer !== undefined) window.clearInterval(timer);
    document.removeEventListener("visibilitychange", onVisibilityChange);
  });

  return { tick };
}
