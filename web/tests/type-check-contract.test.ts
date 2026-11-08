import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const srcRoot = resolve(__dirname, "../src");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

describe("类型检查不被绕开", () => {
  it("带类型的依赖不再被声明成无类型模块，否则 vue-tsc 查不出读不存在字段这类错误", () => {
    const env = readFileSync(resolve(srcRoot, "vite-env.d.ts"), "utf8");
    for (const name of ["pinia", "vue-router", "@arco-design/web-vue", "@/store/modules/route-config"]) {
      expect(env, name).not.toContain(`declare module "${name}"`);
    }
  });

  it("系统只有一个用户，不再按管理员身份隐藏入口", () => {
    const offenders = sourceFiles(srcRoot).filter(file => /user\?\.admin|\bisAdmin\b/.test(readFileSync(file, "utf8")));
    expect(offenders).toEqual([]);
  });
});
