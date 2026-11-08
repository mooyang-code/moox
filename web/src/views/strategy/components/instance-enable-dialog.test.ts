import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";

const api = vi.hoisted(() => ({
  getLogicalAccount: vi.fn(),
  listTradingAccounts: vi.fn(),
  setInstanceEnabled: vi.fn(async () => ({}))
}));
vi.mock("@/api/trade", () => ({ getLogicalAccount: api.getLogicalAccount, listTradingAccounts: api.listTradingAccounts }));
vi.mock("@/api/strategy", () => ({ setInstanceEnabled: api.setInstanceEnabled }));
import InstanceEnableDialog from "./instance-enable-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  open(target: { instance_id: string; logical_account_id: string; factor_tasks?: Record<string, string> }): void;
  handleOk(): boolean;
  canSubmit: boolean;
  typedName: string;
}

const combo = (patch: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: "la-1",
    name: "主组合",
    execution_mode: 2,
    automation_state: "ACTIVE",
    pause_reason: "",
    ready: true,
    members: [{ trading_account_id: "ta-1", enabled: true, priority: 1 }],
    ...patch
  }) as unknown as LogicalAccount;
const member = (environment: 1 | 2) =>
  ({ trading_account_id: "ta-1", execution_mode: 2, live: { environment } }) as unknown as TradingAccount;

let wrapper: VueWrapper;
const openWith = async (logical: LogicalAccount, accounts: TradingAccount[]) => {
  api.getLogicalAccount.mockResolvedValue({ logical_account: logical });
  api.listTradingAccounts.mockResolvedValue({ accounts });
  wrapper = mount(InstanceEnableDialog, { global: { plugins: [ArcoVue] }, attachTo: document.body });
  const vm = wrapper.vm as unknown as DialogVM;
  vm.open({ instance_id: "inst-1", logical_account_id: "la-1", factor_tasks: { crypto: "task-a" } });
  await flushPromises();
  return vm;
};

beforeEach(() => {
  api.getLogicalAccount.mockReset();
  api.listTradingAccounts.mockReset();
  api.setInstanceEnabled.mockClear();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("启用绑了组合账户的实例", () => {
  it("生产组合：写明账户、环境和永久关联，输入组合名称后才能确认", async () => {
    const vm = await openWith(combo(), [member(2)]);
    const text = document.body.textContent || "";
    expect(text).toContain("主组合");
    expect(text).toContain("生产环境");
    expect(text).toContain("因子任务 task-a 会与本实例永久关联");
    expect(text).toContain("输入组合名称「主组合」确认");
    expect(vm.canSubmit).toBe(false);
    vm.typedName = "主组合";
    await flushPromises();
    expect(vm.canSubmit).toBe(true);
    expect(vm.handleOk()).toBe(true);
    await flushPromises();
    expect(api.setInstanceEnabled).toHaveBeenCalledWith("inst-1", true);
  });

  it("测试网组合不要求输入名称；组合暂停时写明不会下单", async () => {
    const vm = await openWith(combo({ automation_state: "PAUSED", pause_reason: "手动" }), [member(1)]);
    const text = document.body.textContent || "";
    expect(text).toContain("测试网");
    expect(text).not.toContain("输入组合名称");
    expect(text).toContain("启用后不会下单");
    expect(vm.canSubmit).toBe(true);
  });

  it("账户读取失败时不能确认，不当成模拟盘放行", async () => {
    api.getLogicalAccount.mockRejectedValue(new Error("交易服务不可用"));
    api.listTradingAccounts.mockResolvedValue({ accounts: [] });
    wrapper = mount(InstanceEnableDialog, { global: { plugins: [ArcoVue] }, attachTo: document.body });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.open({ instance_id: "inst-1", logical_account_id: "la-1" });
    await flushPromises();
    expect(document.body.textContent).toContain("组合账户读取失败：交易服务不可用");
    expect(vm.canSubmit).toBe(false);
    expect(vm.handleOk()).toBe(false);
    expect(api.setInstanceEnabled).not.toHaveBeenCalled();
  });
});
