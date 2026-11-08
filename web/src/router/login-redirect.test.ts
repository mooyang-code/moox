import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "index.ts"), "utf8");

describe("路由守卫跳登录页时带上原地址", () => {
  it("没有会话和取用户信息失败两条路径都带 redirect，不再直接跳裸的 /login", () => {
    expect(source.match(/next\(loginLocation\(to\.fullPath\)\)/g)?.length).toBe(2);
    expect(source).not.toContain('next("/login")');
  });
});
