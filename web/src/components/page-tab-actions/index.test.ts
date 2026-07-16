import { KeepAlive, defineComponent, h, nextTick, ref } from "vue";
import { enableAutoUnmount, mount } from "@vue/test-utils";
import { createPinia, setActivePinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useThemeConfig } from "@/store/modules/theme-config";
import PageTabActions from "./index.vue";

enableAutoUnmount(afterEach);

describe("PageTabActions", () => {
  let target: HTMLDivElement;

  beforeEach(() => {
    vi.stubGlobal("ref", ref);
    document.body.innerHTML = "";
    target = document.createElement("div");
    target.id = "page-tab-actions";
    document.body.appendChild(target);
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("teleports actions to the page tab target when tabs are enabled", async () => {
    const wrapper = mount(PageTabActions, {
      global: { plugins: [createPinia()] },
      slots: { default: '<button data-testid="action">保存</button>' }
    });

    await nextTick();

    expect(wrapper.find('[data-testid="action"]').exists()).toBe(false);
    expect(target.querySelector('[data-testid="action"]')).not.toBeNull();
  });

  it("renders actions inline when tabs are disabled", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    useThemeConfig().isTabs = false;

    const wrapper = mount(PageTabActions, {
      global: { plugins: [pinia] },
      slots: { default: '<button data-testid="action">保存</button>' }
    });

    await nextTick();

    expect(wrapper.find('[data-testid="action"]').exists()).toBe(true);
    expect(target.querySelector('[data-testid="action"]')).toBeNull();
  });

  it("moves actions inline when tabs are disabled after mounting", async () => {
    const pinia = createPinia();
    setActivePinia(pinia);
    const themeStore = useThemeConfig();
    const wrapper = mount(PageTabActions, {
      global: { plugins: [pinia] },
      slots: { default: '<button data-testid="action">保存</button>' }
    });

    await nextTick();
    expect(target.querySelector('[data-testid="action"]')).not.toBeNull();

    themeStore.isTabs = false;
    await nextTick();

    expect(wrapper.find('[data-testid="action"]').exists()).toBe(true);
    expect(target.querySelector('[data-testid="action"]')).toBeNull();
  });

  it("removes teleported storage actions when a kept-alive page is deactivated", async () => {
    const StoragePage = defineComponent({
      name: "StoragePage",
      setup: () => () =>
        h(PageTabActions, null, {
          default: () => h("button", { "data-testid": "storage-action" }, "新建存储")
        })
    });
    const OtherPage = defineComponent({
      name: "OtherPage",
      setup: () => () => h("div", "其他页面")
    });
    const Host = defineComponent({
      props: { showStorage: { type: Boolean, required: true } },
      setup: props => () =>
        h(KeepAlive, null, {
          default: () => (props.showStorage ? h(StoragePage) : h(OtherPage))
        })
    });

    const wrapper = mount(Host, {
      props: { showStorage: true },
      global: { plugins: [createPinia()] }
    });
    await nextTick();
    expect(target.querySelector('[data-testid="storage-action"]')).not.toBeNull();

    await wrapper.setProps({ showStorage: false });
    await nextTick();

    expect(target.querySelector('[data-testid="storage-action"]')).toBeNull();
  });
});
