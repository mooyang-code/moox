import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const webRoot = resolve(__dirname, "..");
const srcRoot = resolve(webRoot, "src");
const buildRoot = resolve(webRoot, "build");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

function declarationFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return declarationFiles(path);
    return entry.name.endsWith(".d.ts") ? [path] : [];
  });
}

describe("类型检查不被绕开", () => {
  it("带类型的依赖与 *.vue 通配符不再被声明成无类型模块，否则 vue-tsc 查不出组件与字段类型错误", () => {
    const env = readFileSync(resolve(srcRoot, "vite-env.d.ts"), "utf8");
    for (const name of [
      "*.vue",
      "pinia",
      "vue-router",
      "@arco-design/web-vue",
      "@/store/modules/route-config",
      "postcss-preset-env"
    ]) {
      expect(env, name).not.toContain(`declare module "${name}"`);
      expect(env, name).not.toContain(`declare module '${name}'`);
    }

    const wildcardOffenders = [...declarationFiles(srcRoot), ...declarationFiles(buildRoot)].filter(file =>
      /declare\s+module\s+["']\*\.vue["']/.test(readFileSync(file, "utf8"))
    );
    expect(wildcardOffenders).toEqual([]);
  });

  it("tsconfig.json 使用 bundler 模块解析以配合 package.json exports 与严格 vue-tsc", () => {
    const raw = readFileSync(resolve(webRoot, "tsconfig.json"), "utf8");
    expect(raw).toMatch(/"moduleResolution"\s*:\s*"bundler"/);
  });

  it("系统只有一个用户，不再按管理员身份隐藏入口", () => {
    const offenders = sourceFiles(srcRoot).filter(file => /user\?\.admin|\bisAdmin\b/.test(readFileSync(file, "utf8")));
    expect(offenders).toEqual([]);
  });
});
