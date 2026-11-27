import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { flushPromises, mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, reactive } from "vue";
import { createMemoryHistory, createRouter } from "vue-router";

const mocks = vi.hoisted(() => ({
  createStrategy: vi.fn(),
  getStrategy: vi.fn(),
  updateStrategy: vi.fn(),
  validateStrategy: vi.fn(),
  invalidateStrategyCatalog: vi.fn(),
  space: { selectedSpaceId: "" }
}));

vi.mock("@/api/strategy", () => ({
  createStrategy: mocks.createStrategy,
  getStrategy: mocks.getStrategy,
  updateStrategy: mocks.updateStrategy,
  validateStrategy: mocks.validateStrategy
}));
vi.mock("@/store/modules/space", () => ({ useSpaceStore: () => mocks.space }));
vi.mock("@/store/modules/strategy", () => ({
  useStrategyStore: () => ({ invalidateStrategyCatalog: mocks.invalidateStrategyCatalog })
}));
vi.mock("@/components/code-editor/async", () => ({
  AsyncCodeEditor: defineComponent({
    name: "AsyncCodeEditor",
    props: ["modelValue", "language", "readOnly", "errorLine"],
    emits: ["update:modelValue", "cursorChange"],
    setup(props, { emit }) {
      return () =>
        h("textarea", {
          class: "code-editor-stub",
          "data-language": props.language,
          "data-readonly": String(Boolean(props.readOnly)),
          "data-error-line": String(props.errorLine ?? 0),
          value: props.modelValue,
          onInput: (e: Event) => emit("update:modelValue", (e.target as HTMLTextAreaElement).value)
        });
    }
  })
}));
vi.mock("@arco-design/web-vue", () => ({ Message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }));

import StrategyEditor from "./index.vue";

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/editor/index.vue"), "utf8");

const stubs = {
  TrialPanel: { props: ["source", "spaceId", "requirement", "managedRequirement"], template: '<div class="trial-stub" />' },
  "a-button": { props: ["disabled", "loading"], template: "<button :disabled='disabled'><slot /></button>" },
  "a-space": { template: "<div><slot /></div>" },
  "a-dropdown": { template: "<div><slot /></div>" },
  "a-doption": { template: "<div><slot /></div>" },
  "a-link": { template: "<a><slot /></a>" },
  "a-alert": { props: ["type"], template: '<div class="alert" :data-type="type"><slot /></div>' },
  "a-grid": { template: "<div><slot /></div>" },
  "a-grid-item": { template: "<div><slot /></div>" },
  "a-form": { template: "<form><slot /></form>" },
  "a-form-item": { template: "<div><slot /></div>" },
  "a-input": { props: ["modelValue"], template: '<input :value="modelValue" />' },
  "a-empty": { template: '<div class="empty" />' },
  "a-descriptions": { template: "<div><slot /></div>" },
  "a-descriptions-item": { template: "<div><slot /></div>" },
  "icon-left": true,
  "icon-down": true
};

async function mountEditor(path = "/strategy/definitions/new") {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: "/strategy/definitions", name: "strategy-definitions", component: { template: "<div />" } },
      { path: "/strategy/definitions/new", name: "strategy-definition-new", component: StrategyEditor },
      { path: "/strategy/definitions/:strategyId/edit", name: "strategy-definition-edit", component: StrategyEditor }
    ]
  });
  await router.push(path);
  const wrapper = mount(StrategyEditor, { global: { plugins: [router], stubs } });
  await flushPromises();
  return { router, wrapper };
}

describe("strategy DSL editor contract", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    mocks.createStrategy.mockReset();
    mocks.getStrategy.mockReset();
    mocks.updateStrategy.mockReset();
    mocks.validateStrategy.mockReset().mockResolvedValue({
      diagnostics: [],
      resolved_json: "",
      trial: null,
      trial_items: [],
      requirement: { pool: false, marketDeclared: false, taskKeys: [], sources: [], problems: [], referenceMarkets: [] }
    });
    mocks.invalidateStrategyCatalog.mockReset();
    mocks.space = reactive({ selectedSpaceId: "" });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("uses AsyncCodeEditor and keeps create and update as definition-only operations", () => {
    expect(source).toContain('import { AsyncCodeEditor } from "@/components/code-editor/async"');
    expect(source).toContain("<AsyncCodeEditor");
    expect(source).toContain('language="yaml"');
    expect(source).not.toContain("<a-textarea");
    expect(source).toContain("createStrategy({ strategy_id: strategyId.value.trim() || undefined, dsl_yaml: submitted })");
    expect(source).toContain("updateStrategy(strategyId.value.trim(), submitted)");
    expect(source).not.toContain("createInstance(");
    expect(source).not.toContain("enabled: true");
  });

  it("protects a dirty draft and offers a server-side trial", () => {
    expect(source).toContain("当前 DSL 尚未保存，确认替换？");
    expect(source).toContain("当前 DSL 尚未保存，确认离开？");
    // 站内离开、刷新、关标签页前的提示收在 useUnsavedGuard 里
    expect(source).toContain("useUnsavedGuard(");
    expect(source).toContain("<TrialPanel");
  });

  it("marks only the submitted snapshot as saved and locks the editor while saving", () => {
    expect(source).toContain("const submitted = source.value;");
    expect(source).toContain("original.value = submitted;");
    expect(source).not.toContain("original.value = source.value;\n    Message.success");
    expect(source).toContain(':read-only="!loaded || loading || saving"');
  });

  it("invalidates the cached definition catalog after a successful save", () => {
    expect(source).toContain("strategyStore.invalidateStrategyCatalog();");
  });

  it("debounces ValidateStrategy and highlights the diagnostic error line in AsyncCodeEditor", async () => {
    mocks.validateStrategy.mockResolvedValueOnce({
      diagnostics: ["策略 DSL 不是合法的 YAML（第 3 行附近）：缺少冒号"],
      resolved_json: "",
      trial: null,
      trial_items: [],
      requirement: null
    });
    const { wrapper } = await mountEditor("/strategy/definitions/new");
    const editor = wrapper.find(".code-editor-stub");
    expect(editor.attributes("data-language")).toBe("yaml");
    expect(mocks.validateStrategy).not.toHaveBeenCalled();

    vi.advanceTimersByTime(400);
    await flushPromises();

    expect(mocks.validateStrategy).toHaveBeenCalledTimes(1);
    expect(wrapper.text()).toContain("第 3 行附近");
    expect(wrapper.find(".code-editor-stub").attributes("data-error-line")).toBe("3");
    wrapper.unmount();
  });

  it("loads an existing strategy definition immediately even when no space is selected", async () => {
    mocks.space.selectedSpaceId = "";
    mocks.getStrategy.mockResolvedValueOnce({
      strategy_id: "s1",
      name: "动量",
      dsl_yaml: "name: 动量\nbar: 1h\nrules:\n  - {id: r1, type: rank}\n",
      dsl_hash: "sha256:abc",
      created_at: "0",
      updated_at: "0"
    });
    const { wrapper } = await mountEditor("/strategy/definitions/s1/edit");
    expect(mocks.getStrategy).toHaveBeenCalledWith("s1");
    expect((wrapper.find(".code-editor-stub").element as HTMLTextAreaElement).value).toContain("name: 动量");
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(mocks.validateStrategy).toHaveBeenCalledWith("name: 动量\nbar: 1h\nrules:\n  - {id: r1, type: rank}\n");
    wrapper.unmount();
  });

  it("resets stale checking state on edits and clears validatedRequirement when switching from edit to new", async () => {
    mocks.getStrategy.mockResolvedValueOnce({
      strategy_id: "s1",
      name: "动量",
      dsl_yaml: "name: 动量\nbar: 1h\nrules:\n  - {id: r1, type: rank}\n",
      dsl_hash: "sha256:abc",
      created_at: "0",
      updated_at: "0"
    });
    const oldRequirement = {
      pool: true,
      marketDeclared: true,
      taskKeys: ["binance:spot:1h"],
      sources: [],
      problems: [],
      referenceMarkets: []
    };
    mocks.validateStrategy.mockResolvedValueOnce({
      diagnostics: [],
      resolved_json: "",
      trial: null,
      trial_items: [],
      requirement: oldRequirement
    });

    const { router, wrapper } = await mountEditor("/strategy/definitions/s1/edit");
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(wrapper.findComponent({ name: "TrialPanel" }).exists()).toBe(false);
    expect(wrapper.find(".trial-stub").exists()).toBe(true);

    // 触发校验失败后继续编辑，防抖窗口内立即回到“等待校验”而非保留错误态
    let resolveInFlight: ((value: unknown) => void) | undefined;
    mocks.validateStrategy.mockImplementationOnce(
      () =>
        new Promise(resolve => {
          resolveInFlight = resolve;
        })
    );
    await wrapper.find(".code-editor-stub").setValue("name: 动量2\nbar: 1h\nrules:\n  - {id: r1, type: rank}\n");
    vi.advanceTimersByTime(400);
    await flushPromises();
    expect(wrapper.text()).toContain("校验中…");

    await wrapper.find(".code-editor-stub").setValue("name: 动量3\nbar: 1h\nrules:\n  - {id: r1, type: rank}\n");
    expect(wrapper.text()).toContain("等待校验");
    expect(wrapper.text()).not.toContain("校验中…");
    resolveInFlight?.({
      diagnostics: ["stale"],
      resolved_json: "",
      trial: null,
      trial_items: [],
      requirement: null
    });
    await flushPromises();
    expect(wrapper.text()).not.toContain("stale");

    // 切到新建定义页时，旧定义的 validatedRequirement 应立刻清空
    const trialStub = wrapper.getComponent(stubs.TrialPanel);
    expect(trialStub.props("requirement")).toEqual(oldRequirement);
    await router.push("/strategy/definitions/new");
    await flushPromises();
    expect(trialStub.props("requirement")).toBeNull();
    wrapper.unmount();
  });
});
