import { readFileSync, readdirSync } from "node:fs";
import { relative, resolve } from "node:path";
import { describe, expect, it } from "vitest";

const viewsRoot = resolve(__dirname, "../src/views");

function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

// 轮询一律用 hooks/usePolling（页面隐藏时暂停、不叠加、切空间安全）。下面几处不是轮询，允许用 setInterval：
// 交易操作的短时跟踪（操作完成就停）、时钟、K 线回放计时器。
const ALLOWED = [
  "trading/accounts/detail/action-tracker.ts",
  "strategy/use-now-clock.ts",
  "ops/monitor/index.vue",
  "data/view-browse/kline-modal.vue"
];

describe("轮询统一用 usePolling", () => {
  it("views 里除白名单外不再出现 setInterval(", () => {
    const offenders = sourceFiles(viewsRoot)
      .map(file => relative(viewsRoot, file))
      .filter(name => !ALLOWED.includes(name))
      .filter(name => readFileSync(resolve(viewsRoot, name), "utf8").includes("setInterval("));
    expect(offenders).toEqual([]);
  });
});
