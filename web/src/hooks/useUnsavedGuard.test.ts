import { mount } from "@vue/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, ref } from "vue";

const guards = vi.hoisted(() => ({ leave: [] as (() => boolean)[], update: [] as (() => boolean)[] }));
vi.mock("vue-router", () => ({
  onBeforeRouteLeave: (fn: () => boolean) => guards.leave.push(fn),
  onBeforeRouteUpdate: (fn: () => boolean) => guards.update.push(fn)
}));
import { useUnsavedGuard } from "./useUnsavedGuard";

afterEach(() => {
  guards.leave.length = 0;
  guards.update.length = 0;
  vi.restoreAllMocks();
});

describe("useUnsavedGuard", () => {
  it("有未保存修改时站内离开要确认、刷新或关标签页时拦下；保存后放行，卸载后不再拦", () => {
    const dirty = ref(true);
    const wrapper = mount(
      defineComponent({
        setup() {
          useUnsavedGuard(() => dirty.value, { leave: "确认离开？" });
          return () => h("div");
        }
      })
    );
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    expect(guards.leave[0]()).toBe(false);
    expect(confirm).toHaveBeenCalledWith("确认离开？");

    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);

    dirty.value = false;
    expect(guards.leave[0]()).toBe(true);
    const clean = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);

    dirty.value = true;
    wrapper.unmount();
    const afterUnmount = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(afterUnmount);
    expect(afterUnmount.defaultPrevented).toBe(false);
  });
});
