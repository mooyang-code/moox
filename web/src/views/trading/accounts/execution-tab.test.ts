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
vi.mock("vue-router", () => ({ useRouter: () => ({ push: vi.fn() }) }));
import ExecutionTab from "./execution-tab.vue";
import JoinDialog from "./join-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface TabVM {
  setStatus(account: TradingAccount, status: "ENABLED" | "DISABLED"): void;
  confirmVisible: boolean;
}
interface JoinVM {
  open(account: TradingAccount): void;
  requestJoin(): void;
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

const confirmButton = (text: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".tx-confirm button")].find(item => item.textContent?.trim() === text);
async function typeName(value: string) {
  const input = document.querySelector<HTMLInputElement>(".tx-confirm input");
  input!.value = value;
  input!.dispatchEvent(new Event("input"));
  await flushPromises();
}

let wrapper: VueWrapper;
const row = { key: "ta-9", account, logical: null, priority: null, enabled: null };
const mountTab = () => {
  wrapper = mount(ExecutionTab, {
    props: { env: "prod", rows: [row], logicals: [combo], accounts: [account], loading: false },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  return wrapper.vm as unknown as TabVM;
};
const mountJoin = () => {
  wrapper = mount(JoinDialog, {
    props: { env: "prod", logicals: [combo], accounts: [account] },
    global: { plugins: [ArcoVue] },
    attachTo: document.body
  });
  const vm = wrapper.vm as unknown as JoinVM;
  vm.open(account);
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

describe("执行账户子页签", () => {
  it("列出所属组合，没加入的写“未加入”并给“加入组合”", async () => {
    mountTab();
    await flushPromises();
    expect(wrapper.text()).toContain("生产散户");
    expect(wrapper.text()).toContain("未加入");
    expect(wrapper.text()).toContain("加入组合");
  });

  it("停用实盘执行账户先确认，不用输入名称；确认后才发请求且不重复弹错", async () => {
    const vm = mountTab();
    vm.setStatus(account, "DISABLED");
    await flushPromises();
    expect(vm.confirmVisible).toBe(true);
    expect(api.updateTradingAccount).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("停用执行账户「生产散户」");
    expect(document.body.textContent).not.toContain("输入执行账户名称");
    confirmButton("停用")?.click();
    await flushPromises();
    expect(api.updateTradingAccount).toHaveBeenCalledWith({ trading_account_id: "ta-9", status: "DISABLED" }, { silent: true });
  });

  it("生产环境加入组合并接管已有持仓：先确认、要输入组合名称", async () => {
    const vm = mountJoin();
    await flushPromises();
    vm.joinForm.adopt = true;
    vm.requestJoin();
    await flushPromises();
    expect(vm.confirmVisible).toBe(true);
    expect(api.addLogicalAccountMember).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("输入组合名称「生产组合」确认");
    expect(confirmButton("确认接管")?.disabled).toBe(true);
    await typeName("生产组合");
    confirmButton("确认接管")?.click();
    await flushPromises();
    expect(api.addLogicalAccountMember).toHaveBeenCalledWith(expect.objectContaining({ adopt_existing_exposure: true }));
  });

  it("不接管时直接加入，不弹确认", async () => {
    const vm = mountJoin();
    await flushPromises();
    vm.requestJoin();
    await flushPromises();
    expect(vm.confirmVisible).toBe(false);
    expect(api.addLogicalAccountMember).toHaveBeenCalledWith(expect.objectContaining({ adopt_existing_exposure: false }));
  });
});
