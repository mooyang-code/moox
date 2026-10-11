import { describe, expect, it } from "vitest";
import { parseInt64 } from "./int64";

describe("parseInt64", () => {
  it("超过安全整数的值保持原样", () => {
    expect(parseInt64("9007199254740993")).toBe("9007199254740993");
  });
  it("接受 INT64 上下界", () => {
    expect(parseInt64("9223372036854775807")).toBe("9223372036854775807");
    expect(parseInt64("-9223372036854775808")).toBe("-9223372036854775808");
  });
  it("越界与非法输入报错", () => {
    expect(() => parseInt64("9223372036854775808")).toThrow("超出 INT64 范围");
    expect(() => parseInt64("-9223372036854775809")).toThrow("超出 INT64 范围");
    expect(() => parseInt64("1.5")).toThrow("整数格式无效");
    expect(() => parseInt64("abc")).toThrow("整数格式无效");
  });
  it("空值为 0，+ 号与前导零被规范化", () => {
    expect(parseInt64("")).toBe("0");
    expect(parseInt64("+007")).toBe("7");
  });
});
