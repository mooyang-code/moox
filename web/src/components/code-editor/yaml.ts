import type { StreamParser } from "@codemirror/language";

interface YAMLState {
  /** 当前行是否已经过了 "键:"，之后的内容是值（表达式）。 */
  inValue: boolean;
}

const FUNCTION_CALL = /^[A-Za-z_][A-Za-z0-9_]*(?=\s*\()/;
const IDENTIFIER = /^[A-Za-z_][A-Za-z0-9_]*/;
const NUMBER = /^-?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?/;
const KEY = /^[A-Za-z_{][A-Za-z0-9_{}]*(?=\s*:(?:\s|$))/;

/**
 * 因子定义的 YAML 高亮：键、注释、字符串、数字；值里的表达式按函数调用、标识符与运算符着色。
 * 只做着色，不做解析：语法与语义以服务端编译（ValidateFactorSpec）为准。
 */
export const factorYAML: StreamParser<YAMLState> = {
  name: "factor-yaml",
  startState: () => ({ inValue: false }),
  token(stream, state) {
    if (stream.sol()) state.inValue = false;
    if (stream.eatSpace()) return null;
    if (stream.peek() === "#") {
      stream.skipToEnd();
      return "comment";
    }
    if (stream.match(/^"(?:[^"\\]|\\.)*"?/) || stream.match(/^'(?:[^']|'')*'?/)) return "string";
    if (!state.inValue) {
      if (stream.match(/^-(?=\s)/)) return "punctuation";
      if (stream.match(KEY)) return "propertyName";
      if (stream.eat(":")) {
        state.inValue = true;
        return "punctuation";
      }
    }
    if (stream.match(/^[|>][-+]?/) && !state.inValue) return "punctuation";
    if (stream.match(NUMBER)) return "number";
    if (stream.match(/^(?:true|false|and|or|not)\b/)) return "keyword";
    if (stream.match(FUNCTION_CALL)) return "function";
    if (stream.match(IDENTIFIER)) return "variableName";
    if (stream.match(/^[(){}[\],]/)) return "bracket";
    if (stream.match(/^(?:==|!=|<=|>=|\*\*|[-+*/%<>?:!])/)) return "operator";
    stream.next();
    return null;
  },
  languageData: { commentTokens: { line: "#" } }
};
