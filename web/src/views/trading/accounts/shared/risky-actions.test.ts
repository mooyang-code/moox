import { describe, expect, it } from "vitest";
import { accountStatusConfirm, leverageConfirm, leverageError, memberChangeConfirm, secretChangeConfirm } from "./risky-actions";

describe("杠杆倍数校验", () => {
  it.each(["0", "1.5", "-1", "abc", "", "  ", "05", "1e2"])("%j 不合法", value => {
    expect(leverageError(value)).not.toBe("");
  });

  it.each(["1", "20", " 125 "])("%j 合法", value => {
    expect(leverageError(value)).toBe("");
  });
});

describe("设置类操作的确认内容", () => {
  it("设杠杆写新旧倍数，说明下次开仓前才设到交易所；生产要输入名称", () => {
    const confirm = leverageConfirm({ instrument: "BTC-USDT-SWAP", current: "3", next: "5" });
    expect(confirm.lines.join("")).toContain("BTC-USDT-SWAP：3× → 5×");
    expect(confirm.lines.join("")).toContain("下次开仓前按新倍数设置到交易所");
    expect(confirm.lines.join("")).not.toContain("立即");
    expect(confirm.nameRequired).toBe(true);
  });

  it("默认杠杆单独写明影响全部标的；没设过的写“未设置”", () => {
    const confirm = leverageConfirm({ instrument: "*", next: "2" });
    expect(confirm.title).toContain("账户默认杠杆");
    expect(confirm.lines.join("")).toContain("未设置 → 2×");
    expect(confirm.lines.join("")).toContain("影响全部标的");
  });

  it("换密钥写新旧密钥名称，生产要输入名称", () => {
    const confirm = secretChangeConfirm({ accountName: "主账户", from: "旧密钥", to: "新密钥" });
    expect(confirm.title).toContain("主账户");
    expect(confirm.lines[0]).toBe("交易所密钥：旧密钥 → 新密钥");
    expect(confirm.nameRequired).toBe(true);
    expect(confirm.danger).toBe(true);
  });

  it("执行账户启停不会下单，不用输入名称", () => {
    expect(accountStatusConfirm({ accountName: "a", enable: true }).nameRequired).toBe(false);
    const disable = accountStatusConfirm({ accountName: "a", enable: false });
    expect(disable.nameRequired).toBe(false);
    expect(disable.danger).toBe(true);
  });
});

describe("组合成员的改动", () => {
  const base = { accountName: "执行一", comboName: "组合甲" };

  it("没有改动、也没接管时不用确认", () => {
    expect(
      memberChangeConfirm({
        ...base,
        original: { enabled: true, priority: 1 },
        next: { enabled: true, priority: 1 },
        adopt: false
      })
    ).toBeNull();
    expect(memberChangeConfirm({ ...base, original: null, next: { enabled: true, priority: 1 }, adopt: false })).toBeNull();
  });

  it("停用、改优先级写出新旧值，不用输入名称", () => {
    const confirm = memberChangeConfirm({
      ...base,
      original: { enabled: true, priority: 1 },
      next: { enabled: false, priority: 2 },
      adopt: false
    });
    expect(confirm?.lines.join("\n")).toContain("启用：是 → 否");
    expect(confirm?.lines.join("\n")).toContain("优先级：1 → 2");
    expect(confirm?.nameRequired).toBe(false);
    expect(confirm?.danger).toBe(true);
  });

  it("接管已有持仓要输入组合名称，并写明仓位可能被调整", () => {
    const confirm = memberChangeConfirm({ ...base, original: null, next: { enabled: true, priority: 1 }, adopt: true });
    expect(confirm?.nameRequired).toBe(true);
    expect(confirm?.lines.join("")).toContain("组合「组合甲」");
    expect(confirm?.lines.join("")).toContain("可能被平掉或加仓");
  });
});
