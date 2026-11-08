import { describe, expect, it } from "vitest";
import { DELIST_READONLY_SECRET_ID, secretDeleteText, secretToggleText } from "./secret-actions";

describe("密钥删除、禁用的确认说明", () => {
  it("写出名称；交易所密钥说明执行账户会变成未就绪", () => {
    const secret = { secret_id: "k1", name: "币安主账户", category: "exchange", status: "active" };
    expect(secretDeleteText(secret)).toContain("删除密钥「币安主账户」");
    expect(secretDeleteText(secret)).toContain("执行账户会变成未就绪");
    expect(secretToggleText(secret)).toContain("禁用密钥「币安主账户」");
    expect(secretToggleText({ ...secret, status: "disabled" })).toBe("启用密钥「币安主账户」？");
  });

  it("下架检测用的只读密钥单独警告", () => {
    const readonly = { secret_id: DELIST_READONLY_SECRET_ID, name: "只读", category: "exchange", status: "active" };
    expect(secretDeleteText(readonly)).toContain("自动下架检测会停止");
    expect(secretToggleText(readonly)).toContain("自动下架检测会停止");
  });

  it("其他类密钥只写名称", () => {
    expect(secretDeleteText({ secret_id: "c1", name: "云账户", category: "cloud" })).toBe("删除密钥「云账户」？删除后无法恢复。");
  });
});
