import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { computed } from "vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AccountEnvironment, ExecutionMode, type LogicalAccount, type ShadowTarget } from "@/api/trade/types";

const api = vi.hoisted(() => ({
  getLogicalAccount: vi.fn(),
  listShadowTargets: vi.fn(),
  confirmInstance: vi.fn()
}));
vi.mock("@/api/trade", () => ({
  getLogicalAccount: api.getLogicalAccount,
  listShadowTargets: api.listShadowTargets
}));
vi.mock("@/api/strategy", () => ({ confirmInstance: api.confirmInstance }));
vi.mock("@/hooks/useDisplayZone", () => ({ useDisplayZone: () => computed(() => "UTC") }));
import InstanceConfirmDialog from "./instance-confirm-dialog.vue";
import { currentSessionTargets, shadowTargetSummary } from "./instance-confirm";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  open(
    instance: { instance_id: string; session_id: string; logical_account_id: string },
    hooks?: { onSettled?: (ok: boolean) => void }
  ): void;
  handleOk(): Promise<boolean>;
  canSubmit: boolean;
  typedName: string;
}

const combo = (patch: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: "la-1",
    name: "主组合",
    execution_mode: ExecutionMode.LIVE,
    environment: AccountEnvironment.PRODUCTION,
    automation_state: "ACTIVE",
    pause_reason: "",
    ready: true,
    members: [{ trading_account_id: "ta-1", enabled: true, priority: 1 }],
    ...patch
  }) as unknown as LogicalAccount;
const shadow = (patch: Partial<ShadowTarget> = {}): ShadowTarget => ({
  target_id: "t-1",
  logical_account_id: "la-1",
  instance_id: "inst-1",
  session_id: "session-2",
  strategy_id: "s-1",
  bar_end_time: "1763168400000",
  equity: "1000",
  weights: [
    { instrument_id: "BTC-USDT", target_weight: "0.6" },
    { instrument_id: "ETH-USDT", target_weight: "0.2", hold: true }
  ],
  targets: [
    { instrument_id: "BTC-USDT", quantity: "0.01" },
    { instrument_id: "ETH-USDT", quantity: "", hold: true }
  ],
  ...patch
});

let wrapper: VueWrapper;
const openWith = async (logical: LogicalAccount, targets: ShadowTarget[], onSettled = vi.fn()) => {
  api.getLogicalAccount.mockResolvedValue({ logical_account: logical });
  api.listShadowTargets.mockResolvedValue({ targets });
  wrapper = mount(InstanceConfirmDialog, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  const vm = wrapper.vm as unknown as DialogVM;
  vm.open({ instance_id: "inst-1", session_id: "session-2", logical_account_id: "la-1" }, { onSettled });
  await flushPromises();
  return vm;
};

beforeEach(() => {
  api.getLogicalAccount.mockReset();
  api.listShadowTargets.mockReset();
  api.confirmInstance.mockReset().mockResolvedValue({});
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("影子目标的展示", () => {
  it("只留本次启用的影子目标，每条写成权重与换算数量", () => {
    const targets = [shadow(), shadow({ target_id: "t-old", session_id: "session-1" })];
    expect(currentSessionTargets(targets, "session-2").map(item => item.target_id)).toEqual(["t-1"]);
    expect(shadowTargetSummary(shadow())).toBe("BTC-USDT 60.00%（0.01）；ETH-USDT 保持");
    expect(shadowTargetSummary(shadow({ weights: [], targets: [] }))).toBe("清仓（目标为空）");
  });
});

describe("确认实盘", () => {
  it("生产组合：先列出本次启用的影子目标，输入组合名称后才能确认，确认带上会话", async () => {
    const onSettled = vi.fn();
    const vm = await openWith(combo(), [shadow(), shadow({ target_id: "t-old", session_id: "session-1" })], onSettled);
    expect(api.listShadowTargets).toHaveBeenCalledWith("inst-1", 20);
    const text = document.body.textContent || "";
    expect(text).toContain("主组合");
    expect(text).toContain("生产环境");
    expect(text).toContain("本次启用最近的影子目标");
    expect(text).toContain("BTC-USDT 60.00%（0.01）；ETH-USDT 保持");
    expect(text).toContain("下一期起策略目标交给组合账户按目标下单");
    expect(text).toContain("输入组合名称「主组合」确认");
    expect(vm.canSubmit).toBe(false);
    vm.typedName = "主组合";
    await flushPromises();
    expect(vm.canSubmit).toBe(true);
    await expect(vm.handleOk()).resolves.toBe(true);
    expect(api.confirmInstance).toHaveBeenCalledWith("inst-1", "session-2");
    expect(onSettled).toHaveBeenCalledWith(true);
  });

  it("测试网组合不要求输入名称；本次启用还没有影子目标时写明，仍可确认", async () => {
    const vm = await openWith(combo({ environment: AccountEnvironment.TESTNET }), [shadow({ session_id: "session-1" })]);
    const text = document.body.textContent || "";
    expect(text).toContain("测试网");
    expect(text).not.toContain("输入组合名称");
    expect(text).toContain("本次启用还没有影子目标");
    expect(vm.canSubmit).toBe(true);
  });

  it("确认被拒（例如策略池预热没完成）时对话框不关，写出原因", async () => {
    const onSettled = vi.fn();
    const vm = await openWith(combo({ environment: AccountEnvironment.TESTNET }), [shadow()], onSettled);
    api.confirmInstance.mockRejectedValue(new Error("策略池预热未完成，不能确认实盘"));
    await expect(vm.handleOk()).resolves.toBe(false);
    await flushPromises();
    expect(document.body.textContent).toContain("确认失败：策略池预热未完成，不能确认实盘");
    expect(onSettled).not.toHaveBeenCalled();
  });

  it("确认时实例的会话已经变了（停用或重新启用过）：写出原因，不提示已确认", async () => {
    const onSettled = vi.fn();
    const vm = await openWith(combo({ environment: AccountEnvironment.TESTNET }), [shadow()], onSettled);
    api.confirmInstance.mockRejectedValue(new Error("实例的会话已变化，请刷新后重试"));
    await expect(vm.handleOk()).resolves.toBe(false);
    await flushPromises();
    expect(document.body.textContent).toContain("确认失败：实例的会话已变化，请刷新后重试");
    expect(onSettled).not.toHaveBeenCalled();
  });

  it("影子目标或账户读取失败时不能确认，不当成“没有影子目标”放行", async () => {
    api.getLogicalAccount.mockResolvedValue({ logical_account: combo({ environment: AccountEnvironment.TESTNET }) });
    api.listShadowTargets.mockRejectedValue(new Error("交易服务不可用"));
    wrapper = mount(InstanceConfirmDialog, { global: { plugins: [ArcoVue] }, attachTo: document.body });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.open({ instance_id: "inst-1", session_id: "session-2", logical_account_id: "la-1" });
    await flushPromises();
    expect(document.body.textContent).toContain("读取失败：交易服务不可用");
    expect(vm.canSubmit).toBe(false);
    await expect(vm.handleOk()).resolves.toBe(false);
    expect(api.confirmInstance).not.toHaveBeenCalled();
  });
});
