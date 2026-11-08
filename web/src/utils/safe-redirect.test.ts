import { describe, expect, it } from "vitest";
import { loginLocation, safeRedirect } from "./safe-redirect";

describe("登录后回到原页面", () => {
  it("站内路径原样返回，带 query 的也保留", () => {
    expect(safeRedirect("/factor/tasks?tab=results")).toBe("/factor/tasks?tab=results");
    expect(safeRedirect(["/home"])).toBe("/home");
  });

  it("外站地址、登录页本身和非字符串一律不接受", () => {
    for (const value of [
      "//evil.example.com",
      "https://evil.example.com",
      "/login",
      "/login?redirect=/home",
      "",
      undefined,
      null,
      1
    ]) {
      expect(safeRedirect(value)).toBe("");
    }
  });
});

describe("路由守卫跳登录页的目标", () => {
  it("带上原地址；首页和不安全的地址不带", () => {
    expect(loginLocation("/factor/tasks?tab=results")).toEqual({
      path: "/login",
      query: { redirect: "/factor/tasks?tab=results" }
    });
    expect(loginLocation("/home")).toBe("/login");
    expect(loginLocation("//evil.example.com")).toBe("/login");
  });
});
