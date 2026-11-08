import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade";

const api = vi.hoisted(() => ({
  updateTradingAccount: vi.fn(async () => ({})),
  addLogicalAccountMember: vi.fn(async () => ({}))
}));
vi.mock("@/api/trade", async () => ({ ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")), ...api }));
vi.mock("./shared/exchange-secrets", () => ({ loadExchangeSecrets: vi.fn(async () => []) }));
import UnjoinedDialog from "./unjoined-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  open(): void;
  openJoin(account: TradingAccount): void;
  setStatus(account: TradingAccount, status: "ENABLED" | "DISABLED"): void;
  requestJoin(): void;
  runPending(): Promise<void>;
  confirmVisible: boolean;
  joinForm: { logicalId: string; priority: number; adopt: boolean };
}

const account = {
  trading_account_id: "ta-9",
  name: "生产散户",
  exchange: 1,
  markets: [2],
  execution_mode: 2,
  status: "ENABLED",
  settlement_asset: "USDT",
  live: { environment: 2 }
} as unknown as TradingAccount;
const combo = {
  logical_account_id: "la-9",
  name: "生产组合",
  execution_mode: 2,
  settlement_asset: "USDT",
  automation_state: "PAUSED",
  members: []
} as unknown as LogicalAccount;

let wrapper: VueWrapper;
const mountIt = (env: "paper" | "prod") => {
  wrapper = mount(UnjoinedDialog, {
    props: { env, rows: [account], logicals: [combo], accounts: [account] },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  const vm = wrapper.vm as unknown as DialogVM;
  vm.open();
  return vm;
};

beforeEach(() => {
  api.updateTradingAccount.mockClear();
  api.addLogicalAccountMember.mockClear();
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("未加入组合的执行账户", () => {
  it("停用实盘执行账户先确认，不用输入名称；确认后才发请求且不重复弹错", async () => {
    const vm = mountIt("prod");
    vm.setStatus(account, "DISABLED");
    await flushPromises();
    expect(vm.confirmVisible).toBe(true);
    expect(api.updateTradingAccount).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("停用执行账户「生产散户」");
    expect(document.body.textContent).not.toContain("输入执行账户名称");
    await vm.runPending();
    expect(api.updateTradingAccount).toHaveBeenCalledWith({ trading_account_id: "ta-9", status: "DISABLED" }, { silent: true });
  });

  it("生产环境加入组合并接管已有持仓：先确认、要输入组合名称", async () => {
    const vm = mountIt("prod");
    vm.openJoin(account);
    await flushPromises();
    vm.joinForm.adopt = true;
    vm.requestJoin();
    await flushPromises();
    expect(vm.confirmVisible).toBe(true);
    expect(api.addLogicalAccountMember).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("输入组合名称「生产组合」确认");
    await vm.runPending();
    expect(api.addLogicalAccountMember).toHaveBeenCalledWith(expect.objectContaining({ adopt_existing_exposure: true }));
  });

  it("不接管时直接加入，不弹确认", async () => {
    const vm = mountIt("prod");
    vm.openJoin(account);
    await flushPromises();
    vm.requestJoin();
    await flushPromises();
    expect(vm.confirmVisible).toBe(false);
    expect(api.addLogicalAccountMember).toHaveBeenCalledWith(expect.objectContaining({ adopt_existing_exposure: false }));
  });
});
