<template>
  <div class="code-editor" :class="{ 'is-readonly': props.readOnly }" :style="rootStyle">
    <div ref="host" class="code-editor__host"></div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { Annotation, Compartment, EditorState } from "@codemirror/state";
import {
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
  placeholder as placeholderExtension
} from "@codemirror/view";
import { bracketMatching, indentUnit } from "@codemirror/language";
import { defaultKeymap, history, historyKeymap, indentLess, indentMore } from "@codemirror/commands";
import { python } from "@codemirror/lang-python";
import { codeEditorThemeExtensions } from "./theme";

defineOptions({ name: "CodeEditor" });

const props = withDefaults(
  defineProps<{
    modelValue: string;
    language?: "python";
    readOnly?: boolean;
    minLines?: number;
    maxLines?: number;
    placeholder?: string;
    ariaLabel?: string;
  }>(),
  { language: "python", readOnly: false, minLines: 8, maxLines: 24, placeholder: "", ariaLabel: "代码编辑器" }
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
  return props.language === "python" ? [python()] : [];
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
        indentUnit.of("    "),
        EditorState.tabSize.of(4),
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

onMounted(createView);

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
  }
);

watch(
  () => props.readOnly,
  readOnly => view?.dispatch({ effects: editableCompartment.reconfigure(editableExtensions(readOnly)) })
);

defineExpose({
  get view() {
    return view;
  }
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
</style>
