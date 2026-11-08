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

describe("没有全局页面缓存", () => {
  it("不再出现全局 keepAlive、缓存名单和“页面切换中”这套（页面内部的 <keep-alive> 不受影响）", () => {
    const offenders = sourceFiles(srcRoot).flatMap(file => {
      const text = readFileSync(file, "utf8");
      return /\bkeepAlive\b|cacheRoutes|setRouteNames|routeLoading/.test(text) ? [relative(srcRoot, file)] : [];
    });
    expect(offenders).toEqual([]);
  });
});
