import { describe, expect, it } from "vitest";
import { DELIST_READONLY_SECRET_ID, secretDeleteText, secretToggleText } from "./secret-actions";

describe("密钥删除、禁用的确认说明", () => {
  it("写出名称；交易所密钥说明已运行的会话不受影响，以及要立即停止该怎么做", () => {
    const secret = { secret_id: "k1", name: "币安主账户", category: "exchange", status: "active" };
    expect(secretDeleteText(secret)).toContain("删除密钥「币安主账户」");
    expect(secretDeleteText(secret)).toContain("已运行的会话不受影响");
    expect(secretToggleText(secret)).toContain("禁用密钥「币安主账户」");
    expect(secretToggleText(secret)).toContain("暂停组合账户或停用执行账户");
    expect(secretToggleText({ ...secret, status: "disabled" })).toBe("启用密钥「币安主账户」？");
  });

  it("下架检测用的只读密钥单独警告；删除时提示只想暂停用禁用", () => {
    const readonly = { secret_id: DELIST_READONLY_SECRET_ID, name: "只读", category: "exchange", status: "active" };
    expect(secretDeleteText(readonly)).toContain("自动下架检测会一直停止");
    expect(secretDeleteText(readonly)).toContain("删除后目前补不回来");
    expect(secretDeleteText(readonly)).toContain("只想暂停请用禁用");
    expect(secretToggleText(readonly)).toContain("自动下架检测会停止");
    expect(secretToggleText(readonly)).not.toContain("只想暂停请用禁用");
  });

  it("其他类密钥只写名称", () => {
    expect(secretDeleteText({ secret_id: "c1", name: "云账户", category: "cloud" })).toBe("删除密钥「云账户」？删除后无法恢复。");
  });
});
