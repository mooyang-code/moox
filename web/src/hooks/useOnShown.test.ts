import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";
import { defineComponent, h, KeepAlive, nextTick, ref } from "vue";
import { onShown } from "./useOnShown";

describe("onShown", () => {
  it("keep-alive 里首次显示只执行一次，之后每次切回再执行", async () => {
    const callback = vi.fn();
    const Shown = defineComponent({ setup: () => (onShown(callback), () => h("div")) });
    const Other = defineComponent({ render: () => h("span") });
    const showFirst = ref(true);
    const Host = defineComponent({ setup: () => () => h(KeepAlive, null, [showFirst.value ? h(Shown) : h(Other)]) });
    const wrapper = mount(Host);
    await nextTick();
    expect(callback).toHaveBeenCalledTimes(1);
    showFirst.value = false;
    await nextTick();
    showFirst.value = true;
    await nextTick();
    expect(callback).toHaveBeenCalledTimes(2);
    wrapper.unmount();
  });

  it("不在 keep-alive 里时挂载执行一次", () => {
    const callback = vi.fn();
    const wrapper = mount(defineComponent({ setup: () => (onShown(callback), () => h("div")) }));
    expect(callback).toHaveBeenCalledTimes(1);
    wrapper.unmount();
  });
});
