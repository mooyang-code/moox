import { EditorView } from "@codemirror/view";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

// Dark+ 配色：独立于 Arco 主题变量，嵌在浅色页面里保持深色。
const palette = {
  background: "#1e1e1e",
  foreground: "#d4d4d4",
  gutter: "#858585",
  activeLine: "#2a2d2e",
  selection: "#264f78",
  cursor: "#aeafad",
  keyword: "#569cd6",
  control: "#c586c0",
  fn: "#dcdcaa",
  string: "#ce9178",
  number: "#b5cea8",
  comment: "#6a9955",
  type: "#4ec9b0",
  variable: "#9cdcfe"
};

const LINE_HEIGHT_PX = 20;

export const codeEditorTheme = EditorView.theme(
  {
    "&": {
      color: palette.foreground,
      backgroundColor: palette.background,
      fontSize: "13px"
    },
    ".cm-scroller": {
      fontFamily: "'SF Mono', 'JetBrains Mono', Menlo, Consolas, monospace",
      lineHeight: `${LINE_HEIGHT_PX}px`,
      minHeight: `calc(var(--code-editor-min-lines, 8) * ${LINE_HEIGHT_PX}px)`,
      maxHeight: `calc(var(--code-editor-max-lines, 24) * ${LINE_HEIGHT_PX}px)`,
      overflow: "auto"
    },
    ".cm-content": { caretColor: palette.cursor, padding: "8px 0" },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: palette.cursor },
    "&.cm-focused": { outline: "none" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": {
      backgroundColor: palette.selection
    },
    ".cm-gutters": {
      backgroundColor: palette.background,
      color: palette.gutter,
      border: "none"
    },
    ".cm-activeLine": { backgroundColor: palette.activeLine },
    ".cm-activeLineGutter": { backgroundColor: palette.activeLine, color: "#c6c6c6" },
    ".cm-matchingBracket": { backgroundColor: "#515c6a", outline: "1px solid #888" },
    ".cm-placeholder": { color: "#6b6b6b" }
  },
  { dark: true }
);

export const codeHighlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.definitionKeyword, t.modifier, t.self, t.bool, t.null], color: palette.keyword },
  { tag: [t.controlKeyword, t.moduleKeyword], color: palette.control },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.macroName], color: palette.fn },
  { tag: [t.definition(t.variableName), t.propertyName, t.variableName], color: palette.variable },
  { tag: [t.className, t.typeName], color: palette.type },
  { tag: [t.string, t.special(t.string), t.regexp], color: palette.string },
  { tag: [t.number, t.integer, t.float], color: palette.number },
  { tag: [t.comment, t.lineComment, t.blockComment], color: palette.comment, fontStyle: "italic" },
  { tag: t.meta, color: palette.fn }
]);

export const codeEditorThemeExtensions = [codeEditorTheme, syntaxHighlighting(codeHighlightStyle)];
