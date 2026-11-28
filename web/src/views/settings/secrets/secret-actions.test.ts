import { describe, expect, it } from "vitest";
import { isDelistReadonlySecret, secretDeleteText, secretToggleText } from "./secret-actions";

describe("密钥删除、禁用的确认说明", () => {
  it("写出名称；交易所密钥说明已运行的会话不受影响，以及要立即停止该怎么做", () => {
    const secret = {
      secret_id: "k1",
      name: "币安主账户",
      category: "exchange",
      provider: "binance",
      extra_config: '{"environment":"production"}',
      status: "active"
    };
    expect(isDelistReadonlySecret(secret)).toBe(false);
    expect(secretDeleteText(secret)).toContain("删除密钥「币安主账户」");
    expect(secretDeleteText(secret)).toContain("已运行的会话不受影响");
    expect(secretToggleText(secret)).toContain("禁用密钥「币安主账户」");
    expect(secretToggleText(secret)).toContain("暂停组合账户或停用执行账户");
    expect(secretToggleText({ ...secret, status: "inactive" })).toBe("启用密钥「币安主账户」？");
  });

  it("按属性识别下架检测用的币安生产只读密钥，不再依赖固定 ID", () => {
    const readonly = {
      secret_id: "sec_random_id",
      name: "币安只读",
      category: "exchange",
      provider: "binance",
      extra_config: '{"environment":"production","readonly":"true"}',
      status: "active"
    };
    expect(isDelistReadonlySecret(readonly)).toBe(true);
    expect(secretDeleteText(readonly)).toContain("自动下架检测会停止");
    expect(secretDeleteText(readonly)).toContain("可随时在密钥管理页重新新建");
    expect(secretDeleteText(readonly)).toContain("只想暂停请用禁用");
    expect(secretToggleText(readonly)).toContain("自动下架检测会停止");
    expect(secretToggleText(readonly)).not.toContain("只想暂停请用禁用");

    // 缺省环境视同生产环境（兼容旧备份导回的只读密钥）；测试网或非币安不算。
    expect(isDelistReadonlySecret({ category: "exchange", provider: "binance", extra_config: '{"readonly":"true"}' })).toBe(true);
    expect(
      isDelistReadonlySecret({
        category: "exchange",
        provider: "binance",
        extra_config: '{"environment":"testnet","readonly":"true"}'
      })
    ).toBe(false);
    expect(
      isDelistReadonlySecret({
        category: "exchange",
        provider: "okx",
        extra_config: '{"environment":"production","readonly":"true"}'
      })
    ).toBe(false);
  });

  it("其他类密钥只写名称", () => {
    expect(secretDeleteText({ secret_id: "c1", name: "云账户", category: "cloud" })).toBe("删除密钥「云账户」？删除后无法恢复。");
  });
});
