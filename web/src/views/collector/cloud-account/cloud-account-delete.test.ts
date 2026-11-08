import { describe, expect, it } from "vitest";
import { cloudAccountDeleteText } from "./cloud-account-delete";

describe("删除云账户的确认正文", () => {
  it("有实例在用时写出个数和后果，并让人先删实例", () => {
    const text = cloudAccountDeleteText(3);
    expect(text).toContain("还有 3 个执行器实例在用");
    expect(text).toContain("在管理台删不掉");
    expect(text).toContain("请先删除这些实例");
  });

  it("没有实例在用时直接说明", () => {
    expect(cloudAccountDeleteText(0)).toBe("没有执行器实例在用这个云账户。");
  });

  it("没查到实例数时写一般影响", () => {
    expect(cloudAccountDeleteText(null)).toContain("请先确认没有实例在用");
  });
});
