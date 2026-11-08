import { flushPromises, mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  listStrategyTemplates: vi.fn(async () => []),
  renderStrategyTemplate: vi.fn(),
  createStrategy: vi.fn(async () => ({ strategy_id: "s-new", name: "新策略" })),
  invalidateStrategyCatalog: vi.fn()
}));
vi.mock("@/api/strategy", () => ({
  listStrategyTemplates: api.listStrategyTemplates,
  renderStrategyTemplate: api.renderStrategyTemplate,
  createStrategy: api.createStrategy
}));
vi.mock("@/store/modules/strategy", () => ({
  useStrategyStore: () => ({ invalidateStrategyCatalog: api.invalidateStrategyCatalog })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn() } }));
import TemplateCreate from "./strategy-template-create.vue";

describe("从模板新建策略", () => {
  it("创建成功后作废策略目录，创建实例的下拉里才能选到新定义", async () => {
    const wrapper = mount(TemplateCreate, { props: { visible: true }, global: { stubs: { "a-drawer": true } } });
    await flushPromises();
    const vm = wrapper.vm as unknown as { dsl: string; create(): Promise<void> };
    vm.dsl = "name: demo\n";
    await vm.create();
    expect(api.createStrategy).toHaveBeenCalledWith({ dsl_yaml: "name: demo\n" });
    expect(api.invalidateStrategyCatalog).toHaveBeenCalledTimes(1);
    expect(wrapper.emitted("created")?.[0]).toEqual(["s-new"]);
  });
});
