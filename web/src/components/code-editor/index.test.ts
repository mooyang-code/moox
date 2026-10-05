import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import { nextTick } from "vue";
import type { EditorView } from "@codemirror/view";
import CodeEditor from "./index.vue";

const wrappers: Array<ReturnType<typeof mount>> = [];

function mountEditor(props: Record<string, unknown> = {}) {
  const wrapper = mount(CodeEditor, { props: { modelValue: "x = 1", ...props }, attachTo: document.body });
  wrappers.push(wrapper);
  return wrapper;
}

function viewOf(wrapper: ReturnType<typeof mount>) {
  return (wrapper.vm as unknown as { view: EditorView }).view;
}

afterEach(() => {
  while (wrappers.length) wrappers.pop()?.unmount();
});

describe("CodeEditor", () => {
  it("TestCodeEditorRendersModelValue", () => {
    const wrapper = mountEditor({ modelValue: "def compute(df):\n    return df" });
    expect(viewOf(wrapper).state.doc.toString()).toBe("def compute(df):\n    return df");
    expect(wrapper.find(".cm-content").exists()).toBe(true);
  });

  it("TestCodeEditorEmitsUpdateOnEdit", () => {
    const wrapper = mountEditor({ modelValue: "a" });
    const view = viewOf(wrapper);
    view.dispatch({ changes: { from: 1, insert: "b" } });
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual(["ab"]);
  });

  it("TestCodeEditorSyncsExternalValue", async () => {
    const wrapper = mountEditor({ modelValue: "a" });
    await wrapper.setProps({ modelValue: "hello" });
    await nextTick();
    expect(viewOf(wrapper).state.doc.toString()).toBe("hello");
    expect(wrapper.emitted("update:modelValue")).toBeUndefined();
  });

  it("TestCodeEditorReadOnly", () => {
    const wrapper = mountEditor({ modelValue: "a", readOnly: true });
    const view = viewOf(wrapper);
    expect(view.state.readOnly).toBe(true);
    expect(wrapper.find(".cm-content").attributes("contenteditable")).toBe("false");
  });

  it("TestCodeEditorDestroysViewOnUnmount", () => {
    const wrapper = mountEditor();
    const view = viewOf(wrapper);
    wrapper.unmount();
    wrappers.pop();
    expect(view.dom.isConnected).toBe(false);
  });

  it("TestCodeEditorHonorsMinMaxLines", () => {
    const wrapper = mountEditor({ minLines: 8, maxLines: 20 });
    const root = wrapper.find(".code-editor").element as HTMLElement;
    expect(root.style.getPropertyValue("--code-editor-min-lines")).toBe("8");
    expect(root.style.getPropertyValue("--code-editor-max-lines")).toBe("20");
  });

  it("exposes an async entry so CodeMirror stays out of the main chunk", async () => {
    const mod = await import("./async");
    expect(mod.AsyncCodeEditor).toBeTruthy();
  });
});
