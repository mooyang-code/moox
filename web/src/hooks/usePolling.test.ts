import { describe, expect, it, vi } from "vitest";
import { createApp, defineComponent, h, KeepAlive, ref } from "vue";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { usePolling } from "./usePolling";

describe("usePolling", () => {
  it("不会被切换空间前永不落定的在途任务永久卡住", async () => {
    setSelectedSpaceIdCache("space-a");
    let calls = 0;
    const task = vi.fn(() => {
      calls += 1;
      return calls === 1 ? new Promise<void>(() => undefined) : Promise.resolve();
    });
    let tick: () => Promise<void> = async () => undefined;
    const app = createApp(
      defineComponent({
        setup() {
          tick = usePolling(task, 60_000).tick;
          return () => h("div");
        }
      })
    );
    app.mount(document.createElement("div"));
    void tick(); // 第一次：旧空间请求被请求层丢弃，永不落定
    await Promise.resolve();
    await tick();
    expect(task).toHaveBeenCalledTimes(1); // 同一空间内仍受“不叠加”保护

    setSelectedSpaceIdCache("space-b");
    await tick();
    expect(task).toHaveBeenCalledTimes(2); // 切换空间后释放，轮询恢复
    app.unmount();
    setSelectedSpaceIdCache("");
  });

  it("在页面内部的 keep-alive 里失活时暂停，重新激活时立即补一次", async () => {
    vi.useFakeTimers();
    const task = vi.fn(async () => undefined);
    const Child = defineComponent({
      setup() {
        usePolling(task, 1000);
        return () => h("div");
      }
    });
    const Other = defineComponent({ setup: () => () => h("span") });
    const show = ref(true);
    const app = createApp(defineComponent({ setup: () => () => h(KeepAlive, null, [show.value ? h(Child) : h(Other)]) }));
    app.mount(document.createElement("div"));
    // 首次挂载在 keep-alive 里也算一次激活：立即跑一次，之后按间隔
    await vi.advanceTimersByTimeAsync(1000);
    expect(task).toHaveBeenCalledTimes(2);

    show.value = false;
    await vi.advanceTimersByTimeAsync(3000);
    expect(task).toHaveBeenCalledTimes(2); // 失活期间不跑

    show.value = true;
    await vi.advanceTimersByTimeAsync(0);
    expect(task).toHaveBeenCalledTimes(3); // 激活时补一次
    app.unmount();
    vi.useRealTimers();
  });
});
