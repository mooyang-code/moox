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
    const wrapper = mountEditor({ modelValue: "outputs:\n  x: close" });
    expect(viewOf(wrapper).state.doc.toString()).toBe("outputs:\n  x: close");
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

  it("marks the diagnostic line and clears it", async () => {
    const wrapper = mountEditor({ modelValue: "outputs:\n  x: nope(close)\n", errorLine: 2 });
    await nextTick();
    expect(wrapper.findAll(".cm-error-line")).toHaveLength(1);
    await wrapper.setProps({ errorLine: 0 });
    await nextTick();
    expect(wrapper.findAll(".cm-error-line")).toHaveLength(0);
    await wrapper.setProps({ errorLine: 99 });
    await nextTick();
    expect(wrapper.findAll(".cm-error-line")).toHaveLength(0);
  });

  it("inserts text at the cursor and refuses when read-only", async () => {
    const wrapper = mountEditor({ modelValue: "outputs:\n  x: " });
    const view = viewOf(wrapper);
    view.dispatch({ selection: { anchor: view.state.doc.length } });
    const api = wrapper.vm as unknown as { insertAtCursor(text: string): boolean };
    expect(api.insertAtCursor("ts_mean(close, 20)")).toBe(true);
    expect(view.state.doc.toString()).toBe("outputs:\n  x: ts_mean(close, 20)");
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual(["outputs:\n  x: ts_mean(close, 20)"]);
    await wrapper.setProps({ readOnly: true });
    expect(api.insertAtCursor("more")).toBe(false);
  });

  it("highlights YAML keys, comments and expression calls", async () => {
    const { factorYAML } = await import("./yaml");
    const { StringStream } = await import("@codemirror/language");
    const tokens = (line: string) => {
      const state = factorYAML.startState!(2);
      const stream = new StringStream(line, 2, 2);
      const out: string[] = [];
      while (!stream.eol()) {
        const style = factorYAML.token(stream, state);
        if (style) out.push(`${style}:${stream.current()}`);
        stream.start = stream.pos;
      }
      return out;
    };
    expect(tokens("  bias_{window}: close / ts_mean(close, window) # 注释")).toEqual([
      "propertyName:bias_{window}",
      "punctuation::",
      "variableName:close",
      "operator:/",
      "function:ts_mean",
      "bracket:(",
      "variableName:close",
      "bracket:,",
      "variableName:window",
      "bracket:)",
      "comment:# 注释"
    ]);
    expect(tokens("params: {window: [5, 20]}")).toContain("number:20");
  });

  it("exposes an async entry so CodeMirror stays out of the main chunk", async () => {
    const mod = await import("./async");
    expect(mod.AsyncCodeEditor).toBeTruthy();
  });
});
