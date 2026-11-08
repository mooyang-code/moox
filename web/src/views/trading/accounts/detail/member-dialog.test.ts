import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TradeResponseError, type LogicalAccount, type TradingAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({ addLogicalAccountMember: vi.fn() }));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
import MemberDialog from "./member-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  openAdd(): void;
  openEdit(member: { trading_account_id: string; enabled: boolean; priority: number }, name: string): void;
  save(): Promise<boolean>;
  visible: boolean;
  confirmVisible: boolean;
  form: { enabled: boolean; adopt_existing_exposure: boolean };
  adoptionNeeded: boolean;
}

const logical = {
  logical_account_id: "la-1",
  name: "组合",
  execution_mode: 2,
  settlement_asset: "USDT",
  automation_state: "PAUSED",
  members: [{ trading_account_id: "ta-1", enabled: false, priority: 1 }]
} as LogicalAccount;

const confirmButton = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".tx-confirm button")].find(item => item.textContent?.trim() === text);
async function typeName(value: string) {
  const input = document.querySelector<HTMLInputElement>(".tx-confirm input");
  input!.value = value;
  input!.dispatchEvent(new Event("input"));
  await flushPromises();
}

let wrapper: VueWrapper;
beforeEach(() => mocks.addLogicalAccountMember.mockReset());
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("编辑组合成员", () => {
  it("重新启用已停用成员时后端要求接管：提示并显示“接管”勾选，勾上后重试带上接管", async () => {
    wrapper = mount(MemberDialog, {
      props: { logical, candidates: [], env: "paper" },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.openEdit({ trading_account_id: "ta-1", enabled: false, priority: 1 }, "执行账户一");
    await flushPromises();
    expect(document.body.textContent).not.toContain("接管账户里已有的持仓");
    vm.form.enabled = true;
    mocks.addLogicalAccountMember.mockRejectedValueOnce(
      new TradeResponseError({ ret_info: { code: 20, msg: "执行账户上已有持仓或挂单，加入组合需要确认接管这些已有仓位" } })
    );
    expect(await vm.save()).toBe(false);
    await flushPromises();
    expect(vm.adoptionNeeded).toBe(true);
    expect(document.body.textContent).toContain("重新启用要确认接管");
    expect(document.body.textContent).toContain("接管账户里已有的持仓");

    vm.form.adopt_existing_exposure = true;
    mocks.addLogicalAccountMember.mockResolvedValueOnce({});
    expect(await vm.save()).toBe(true);
    expect(mocks.addLogicalAccountMember).toHaveBeenLastCalledWith(
      expect.objectContaining({ trading_account_id: "ta-1", enabled: true, adopt_existing_exposure: true })
    );
  });

  it("生产组合里改优先级先确认，不用输入名称；确认后才保存并关掉对话框", async () => {
    wrapper = mount(MemberDialog, {
      props: {
        logical: { ...logical, members: [{ trading_account_id: "ta-1", enabled: true, priority: 1 }] },
        candidates: [],
        env: "prod"
      },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.openEdit({ trading_account_id: "ta-1", enabled: true, priority: 1 }, "执行账户一");
    await flushPromises();
    (vm.form as unknown as { priority: number }).priority = 3;
    expect(await vm.save()).toBe(false);
    await flushPromises();
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
    expect(vm.confirmVisible).toBe(true);
    expect(document.body.textContent).toContain("优先级：1 → 3");
    expect(document.body.textContent).not.toContain("输入组合名称");
    mocks.addLogicalAccountMember.mockResolvedValueOnce({});
    confirmButton("保存")?.click();
    await flushPromises();
    expect(mocks.addLogicalAccountMember).toHaveBeenCalledWith(
      expect.objectContaining({ trading_account_id: "ta-1", priority: 3 })
    );
    expect(vm.visible).toBe(false);
  });

  it("生产组合里新增成员并接管已有持仓：确认框要输入组合名称", async () => {
    const candidate = { trading_account_id: "ta-2", name: "执行账户二", exchange: 1, markets: [2] } as unknown as TradingAccount;
    wrapper = mount(MemberDialog, {
      props: { logical, candidates: [candidate], env: "prod" },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.openAdd();
    await flushPromises();
    vm.form.adopt_existing_exposure = true;
    expect(await vm.save()).toBe(false);
    await flushPromises();
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("接管「执行账户二」已有的持仓");
    expect(document.body.textContent).toContain("不在策略目标里的会被平掉");
    expect(document.body.textContent).toContain("输入组合名称「组合」确认");
    expect(confirmButton("确认接管")?.disabled).toBe(true);
    confirmButton("确认接管")?.click();
    await flushPromises();
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
    mocks.addLogicalAccountMember.mockResolvedValueOnce({});
    await typeName("组合");
    confirmButton("确认接管")?.click();
    await flushPromises();
    expect(mocks.addLogicalAccountMember).toHaveBeenCalledWith(
      expect.objectContaining({ trading_account_id: "ta-2", adopt_existing_exposure: true })
    );
  });

  it("新增成员不接管时不用确认，直接保存", async () => {
    const candidate = { trading_account_id: "ta-2", name: "执行账户二", exchange: 1, markets: [2] } as unknown as TradingAccount;
    wrapper = mount(MemberDialog, {
      props: { logical, candidates: [candidate], env: "prod" },
      global: { plugins: [ArcoVue] },
      attachTo: document.body
    });
    const vm = wrapper.vm as unknown as DialogVM;
    vm.openAdd();
    await flushPromises();
    mocks.addLogicalAccountMember.mockResolvedValueOnce({});
    expect(await vm.save()).toBe(true);
    expect(vm.confirmVisible).toBe(false);
  });
});
