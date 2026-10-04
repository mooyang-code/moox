import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const workbenchPath = path.resolve(__dirname, "index.vue");
const routePath = path.resolve(__dirname, "../../../router/route.ts");
const read = (filePath: string) => fs.readFileSync(filePath, "utf8");

describe("账户工作台契约", () => {
  it("只保留执行账户和组合账户两个一级页签并按视图懒加载子页面", () => {
    const source = read(workbenchPath);
    expect(source).toContain('label: "执行账户"');
    expect(source).toContain('label: "组合账户"');
    expect(source).not.toContain('label: "交易账户"');
    expect(source).not.toContain('label: "策略账户"');
    expect(source).toContain('type WorkbenchView = "trading" | "strategy"');
    expect(source).toContain("v-if=\"activeView === 'trading'\"");
    expect(source).toContain('<LogicalAccounts v-else :embedded="true" />');
    expect(source).toContain('value === "strategy" ? "strategy" : "trading"');
    expect(source).not.toContain("<h2>");
  });

  it("同步工作台查询参数并清理互斥的子视图参数", () => {
    const source = read(workbenchPath);
    expect(source).toContain('view: view === "strategy" ? "strategy" : undefined');
    expect(source).toContain('mode: view === "strategy" ? undefined : route.query.mode');
    expect(source).toContain('logical_account_id: view === "trading" ? undefined : route.query.logical_account_id');
    expect(source).toContain('router.replace({ path: "/trading/accounts", query })');
  });

  it("将旧组合账户入口重定向到统一工作台", () => {
    const source = read(routePath);
    expect(source).toContain('path: "/trading/accounts"');
    expect(source).toContain('path: "/trading/logical-accounts"');
    expect(source).toContain('view: "strategy"');
    expect(source).toContain("...to.query");
    expect(source).not.toContain(
      'path: "/trading/logical-accounts",\n        name: "trading-logical-accounts",\n        component:'
    );
  });
});
