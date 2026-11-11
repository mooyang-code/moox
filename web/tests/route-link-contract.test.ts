import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const srcRoot = resolve(__dirname, "../src");
const read = (relative: string) => readFileSync(resolve(srcRoot, relative), "utf8");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

/** 注册过的页面路径：静态路由、菜单条目和 404 兜底。带参数的路由（如 /strategy/detail/:instanceId）只登记前缀。 */
function knownPaths(): { exact: Set<string>; prefixes: string[] } {
  // /404 没有单独注册，是有意落到兜底的 not-found 路由
  const exact = new Set<string>(["/", "/login", "/404"]);
  const prefixes: string[] = [];
  const add = (path: string) => {
    const index = path.indexOf("/:");
    // 根上的参数路由是 404 兜底（/:pathMatch(.*)*），不能当成前缀，否则任何路径都算存在
    if (index === 0) return;
    if (index > 0) prefixes.push(path.slice(0, index + 1));
    else exact.add(path);
  };
  for (const match of read("router/route.ts").matchAll(/path:\s*"([^"]+)"/g)) add(match[1]);
  for (const match of read("api/modules/system/static-menu.ts").matchAll(
    /(?:menu|directory)\(\s*"[^"]+",\s*"[^"]+",\s*"([^"]+)"/g
  ))
    add(match[1]);
  for (const match of read("router/index.ts").matchAll(/path:\s*"([^"]+)"/g)) add(match[1]);
  return { exact, prefixes };
}

/**
 * 源码里写死的站内跳转路径：router.push/replace 的字面量、{ path: "/..." }、<router-link to="/...">、href="#/..."。
 * 拼接出来的路径（模板字符串里带变量）不在检查范围内。
 */
function literalLinks(source: string): string[] {
  const patterns = [
    /\b(?:push|replace)\(\s*["'](\/[^"'?#\s]*)/g,
    /\bpath:\s*["'](\/[^"'?#\s]*)["']/g,
    /\bto="(\/[^"?#\s]*)"/g,
    /["'`]#(\/[^"'`?#\s$]*)/g
  ];
  return patterns.flatMap(pattern => [...source.matchAll(pattern)].map(match => match[1]));
}

describe("站内跳转的路径都有对应的路由", () => {
  it("没有跳到不存在页面的字面量路径", () => {
    const { exact, prefixes } = knownPaths();
    const missing: string[] = [];
    for (const file of sourceFiles(resolve(srcRoot, "views")).concat(
      sourceFiles(resolve(srcRoot, "components")),
      sourceFiles(resolve(srcRoot, "layout"))
    )) {
      for (const path of literalLinks(readFileSync(file, "utf8"))) {
        const normalized = path.length > 1 ? path.replace(/\/$/, "") : path;
        // 以 / 结尾的字面量是后面要拼参数的前缀（例如 `#/trading/accounts/${id}`），按原样和参数路由的前缀比
        if (exact.has(normalized) || prefixes.some(prefix => normalized.startsWith(prefix) || path.startsWith(prefix))) continue;
        missing.push(`${file.replace(`${srcRoot}/`, "")}: ${path}`);
      }
    }
    expect(missing).toEqual([]);
  });

  it("错误页只保留 404 兜底", () => {
    expect(readdirSync(resolve(srcRoot, "views/error"))).toEqual(["404.vue"]);
    const routes = read("router/route.ts");
    expect(routes).not.toMatch(/path:\s*"\/(401|500)"/);
    expect(routes).toContain('import("@/views/error/404.vue")');
  });
});
