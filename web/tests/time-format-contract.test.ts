import { readFileSync, readdirSync } from "node:fs";
import { relative, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const srcRoot = resolve(__dirname, "../src");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

/** 文件里命中正则的行，写成“相对路径:行号” */
function hits(pattern: RegExp, skip: (file: string) => boolean = () => false): string[] {
  return sourceFiles(srcRoot).flatMap(file => {
    const name = relative(srcRoot, file);
    if (skip(name)) return [];
    return readFileSync(file, "utf8")
      .split("\n")
      .flatMap((line, index) => (pattern.test(line) ? [`${name}:${index + 1}`] : []));
  });
}

// 时间统一走 utils/time-zone.ts（按空间时区、带数字偏移后缀）；toLocaleString 只用于数字格式化。
// 以后新增数字格式化时加进这个白名单。
const NUMBER_FORMATTERS = [
  "views/data/view-browse/kline-modal.vue",
  "views/trading/accounts/shared/format.ts",
  "views/trading/accounts/detail/pnl-model.ts"
];

describe("时间显示只走统一的时区工具", () => {
  it("除时间工具外不再用 toLocaleTimeString、toLocaleDateString（它们按浏览器本地时区）", () => {
    expect(hits(/toLocale(Time|Date)String\(/, name => name === "utils/time-zone.ts")).toEqual([]);
  });

  it("toLocaleString 只出现在白名单里的数字格式化", () => {
    expect(hits(/toLocaleString\(/, name => NUMBER_FORMATTERS.includes(name))).toEqual([]);
  });
});
