import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap(name => {
    const path = join(dir, name);
    return statSync(path).isDirectory() ? walk(path) : [path];
  });
}

// 删除或改名组件后，仍引用它的页面在 vue-tsc 与单测里都不一定报错，只有生产构建才失败：这里直接核对策略模块里 @/ 开头的 .vue 与 .ts 引用都存在。
describe("策略模块的引用", () => {
  it("每个 @/ 引用的文件都存在", () => {
    const root = resolve(process.cwd(), "src");
    const missing: string[] = [];
    for (const file of walk(join(root, "views/strategy")).filter(path => /\.(vue|ts)$/.test(path))) {
      for (const match of readFileSync(file, "utf8").matchAll(/from\s+"@\/([^"]+)"/g)) {
        const target = join(root, match[1]);
        if (
          ![target, `${target}.ts`, `${target}.vue`, join(target, "index.ts")].some(
            candidate => existsSync(candidate) && statSync(candidate).isFile()
          )
        )
          missing.push(`${file.replace(root, "src")} → @/${match[1]}`);
      }
    }
    expect(missing).toEqual([]);
  });
});
