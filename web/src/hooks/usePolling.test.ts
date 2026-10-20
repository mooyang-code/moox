import { describe, expect, it, vi } from "vitest";
import { createApp, defineComponent, h } from "vue";
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
});
