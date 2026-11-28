import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it } from "vitest";
import { defineComponent, h, onMounted, onUnmounted } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";
import TitleTabsWorkbench from "./workbench.vue";

describe("TitleTabsWorkbench", () => {
  it("按 URL 渲染页签，切走即卸载不使用 keep-alive", async () => {
    const events: string[] = [];
    const First = defineComponent({
      setup() {
        onMounted(() => events.push("mount:first"));
        onUnmounted(() => events.push("unmount:first"));
        return () => h("div", { class: "pane" }, "first");
      }
    });
    const Second = defineComponent({
      setup() {
        onMounted(() => events.push("mount:second"));
        onUnmounted(() => events.push("unmount:second"));
        return () => h("div", { class: "pane" }, "second");
      }
    });
    const Host = defineComponent({
      render: () =>
        h(TitleTabsWorkbench, {
          label: "示例工作台",
          routeTarget: { path: "/bench" },
          tabs: [
            { key: "first", label: "第一页", component: First },
            { key: "second", label: "第二页", component: Second }
          ]
        })
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: "/bench", component: Host }]
    });
    await router.push({ path: "/bench", query: { foo: "bar" } });
    const wrapper = mount(Host, { global: { plugins: [router] } });
    await flushPromises();

    expect(wrapper.find(".pane").text()).toBe("first");
    expect(events).toEqual(["mount:first"]);

    await wrapper
      .findAll("button.page-title-tab")
      .find(btn => btn.text() === "第二页")!
      .trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.query).toEqual({ tab: "second" });
    expect(wrapper.find(".pane").text()).toBe("second");
    expect(events).toEqual(["mount:first", "unmount:first", "mount:second"]);
  });
});
