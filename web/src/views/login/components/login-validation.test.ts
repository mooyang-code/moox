import { describe, expect, it } from "vitest";
import { validatePasswordInput } from "./login-validation";

describe("validatePasswordInput", () => {
  it("accepts passwords with symbols and long passwords up to 72 bytes", () => {
    expect(validatePasswordInput("Admin!123")).toBeNull();
    expect(validatePasswordInput("a".repeat(72))).toBeNull();
    expect(validatePasswordInput("密码-pass!")).toBeNull();
  });

  it("rejects empty passwords and passwords above 72 bytes", () => {
    expect(validatePasswordInput("")).toBe("请输入密码");
    expect(validatePasswordInput("   ")).toBe("请输入密码");
    expect(validatePasswordInput("a".repeat(73))).toContain("72");
    expect(validatePasswordInput("密".repeat(25))).toContain("72"); // 75 字节
    expect(validatePasswordInput("ab\u0000cd")).toContain("空字符");
  });
});
