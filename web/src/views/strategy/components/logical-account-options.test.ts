import { describe, expect, it } from "vitest";
import type { LogicalAccount } from "@/api/trade/types";
import { logicalAccountOptions, resolvedMarket } from "./logical-account-options";

const account = (patch: Partial<LogicalAccount>) =>
  ({
    logical_account_id: "a",
    name: "组合",
    control_mode: 1,
    market_type: 2,
    ready: true,
    members: [],
    ...patch
  }) as LogicalAccount;

describe("策略绑定组合账户选项", () => {
  it("只列策略驱动且市场一致的账户", () => {
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "swap" }),
        account({ logical_account_id: "spot", market_type: 1 }),
        account({ logical_account_id: "manual", control_mode: 2 })
      ],
      2
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["swap"]);
  });

  it("已被其他实例绑定的显示为不可选并写明实例", () => {
    const [option] = logicalAccountOptions([account({ owner_instance_id: "inst-1" })]);
    expect(option.disabledReason).toContain("inst-1");
    expect(option.label).toContain("inst-1");
  });

  it("从校验结果读市场", () => {
    expect(resolvedMarket('{"market_type":"swap"}')).toBe(2);
    expect(resolvedMarket("bad")).toBeUndefined();
    expect(resolvedMarket()).toBeUndefined();
  });
});
