import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TradeResponseError, type LogicalAccount } from "@/api/trade";

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
  openEdit(member: { trading_account_id: string; enabled: boolean; priority: number }, name: string): void;
  save(): Promise<boolean>;
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

let wrapper: VueWrapper;
beforeEach(() => mocks.addLogicalAccountMember.mockReset());
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("编辑组合成员", () => {
  it("重新启用已停用成员时后端要求接管：提示并显示“接管”勾选，勾上后重试带上接管", async () => {
    wrapper = mount(MemberDialog, {
      props: { logical, candidates: [] },
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
});
