<template>
  <div class="code-editor" :class="{ 'is-readonly': props.readOnly }" :style="rootStyle">
    <div ref="host" class="code-editor__host"></div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Annotation, Compartment, EditorState, RangeSetBuilder } from "@codemirror/state";
import {
  Decoration,
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder as placeholderExtension
} from "@codemirror/view";
import { bracketMatching, indentUnit, StreamLanguage } from "@codemirror/language";
import { defaultKeymap, history, historyKeymap, indentLess, indentMore } from "@codemirror/commands";
import { codeEditorThemeExtensions } from "./theme";
import { factorYAML } from "./yaml";

defineOptions({ name: "CodeEditor" });

const props = withDefaults(
  defineProps<{
    modelValue: string;
    language?: "yaml" | "plain";
    readOnly?: boolean;
    /** 要标红的行（从 1 开始），用于显示编译诊断；0 表示不标。 */
    errorLine?: number;
    minLines?: number;
    maxLines?: number;
    placeholder?: string;
    ariaLabel?: string;
  }>(),
  { language: "yaml", readOnly: false, errorLine: 0, minLines: 8, maxLines: 24, placeholder: "", ariaLabel: "代码编辑器" }
);

const emit = defineEmits<{
  "update:modelValue": [value: string];
  cursorChange: [position: { line: number; column: number }];
}>();

const host = ref<HTMLElement | null>(null);
let view: EditorView | undefined;

// 外部写入不得回触发 update:modelValue。
const externalChange = Annotation.define<boolean>();
const editableCompartment = new Compartment();
const errorLineCompartment = new Compartment();
const errorLineDecoration = Decoration.line({ class: "cm-error-line" });

const rootStyle = computed(() => ({
  "--code-editor-min-lines": String(props.minLines),
  "--code-editor-max-lines": String(Math.max(props.minLines, props.maxLines))
}));

// Esc 之后 Tab 不再被编辑器截获，键盘用户可以离开编辑区。
let tabEscaped = false;

function editableExtensions(readOnly: boolean) {
  return [EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)];
}

function languageExtensions() {
  return props.language === "yaml" ? [StreamLanguage.define(factorYAML)] : [];
}

function errorLineExtensions(state: EditorState | undefined, line: number) {
  if (!state || line < 1 || line > state.doc.lines) return [];
  const builder = new RangeSetBuilder<Decoration>();
  const at = state.doc.line(line);
  builder.add(at.from, at.from, errorLineDecoration);
  return [EditorView.decorations.of(builder.finish())];
}

function createView() {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightActiveLine(),
        drawSelection(),
        history(),
        bracketMatching(),
        indentUnit.of(props.language === "yaml" ? "  " : "    "),
        EditorState.tabSize.of(props.language === "yaml" ? 2 : 4),
        keymap.of([
          {
            key: "Escape",
            run: () => {
              tabEscaped = true;
              return true;
            }
          },
          { key: "Tab", run: v => !tabEscaped && indentMore(v) },
          { key: "Shift-Tab", run: v => !tabEscaped && indentLess(v) },
          ...defaultKeymap,
          ...historyKeymap
        ]),
        EditorView.domEventHandlers({
          keydown: event => {
            if (!["Tab", "Escape", "Shift"].includes(event.key)) tabEscaped = false;
            return false;
          },
          blur: () => {
            tabEscaped = false;
            return false;
          }
        }),
        languageExtensions(),
        codeEditorThemeExtensions,
        EditorView.contentAttributes.of({ "aria-label": props.ariaLabel }),
        placeholderExtension(props.placeholder),
        editableCompartment.of(editableExtensions(props.readOnly)),
        errorLineCompartment.of([]),
        EditorView.updateListener.of(update => {
          if (update.docChanged && !update.transactions.some(tr => tr.annotation(externalChange))) {
            emit("update:modelValue", update.state.doc.toString());
          }
          if (update.selectionSet || update.docChanged) {
            const head = update.state.selection.main.head;
            const line = update.state.doc.lineAt(head);
            emit("cursorChange", { line: line.number, column: head - line.from + 1 });
          }
        })
      ]
    })
  });
}

onMounted(() => {
  createView();
  applyErrorLine();
});

function applyErrorLine() {
  view?.dispatch({ effects: errorLineCompartment.reconfigure(errorLineExtensions(view.state, props.errorLine)) });
}

watch(() => props.errorLine, applyErrorLine);

onBeforeUnmount(() => {
  view?.destroy();
  view = undefined;
});

watch(
  () => props.modelValue,
  value => {
    if (!view || value === view.state.doc.toString()) return;
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: value },
      annotations: externalChange.of(true)
    });
    applyErrorLine();
  }
);

watch(
  () => props.readOnly,
  readOnly => view?.dispatch({ effects: editableCompartment.reconfigure(editableExtensions(readOnly)) })
);

/** 在光标处插入文本（函数参考面板用），插入后光标移到文本末尾。 */
function insertAtCursor(text: string) {
  if (!view || props.readOnly) return false;
  const { from, to } = view.state.selection.main;
  view.dispatch({ changes: { from, to, insert: text }, selection: { anchor: from + text.length } });
  view.focus();
  return true;
}

defineExpose({
  get view() {
    return view;
  },
  insertAtCursor
});
</script>

<style scoped lang="scss">
.code-editor {
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #1e1e1e;
}

.code-editor__host {
  min-width: 0;
}

.code-editor :deep(.cm-error-line) {
  background: rgba(244, 135, 113, 0.18);
}
</style>
