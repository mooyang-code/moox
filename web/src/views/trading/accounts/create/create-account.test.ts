import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({
  createPaperSimulation: vi.fn(),
  createTradingAccount: vi.fn(),
  createLogicalAccount: vi.fn(),
  addLogicalAccountMember: vi.fn()
}));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
vi.mock("../shared/exchange-secrets", () => ({ loadExchangeSecrets: vi.fn(async () => []) }));
import CreateAccount from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  open(kind: string): void;
  submit(): Promise<void>;
  form: Record<string, unknown>;
}

let wrapper: VueWrapper;
const existing = {
  logical_account_id: "la-existing",
  name: "已有组合",
  execution_mode: 1,
  settlement_asset: "USDT",
  automation_state: "PAUSED",
  members: [{ trading_account_id: "a", enabled: true, priority: 2 }]
} as LogicalAccount;

async function openDialog(logicals: LogicalAccount[] = [existing]) {
  wrapper = mount(CreateAccount, { props: { logicals, accounts: [] }, global: { plugins: [ArcoVue] } });
  const vm = wrapper.vm as unknown as DialogVM;
  vm.open("paper");
  Object.assign(vm.form, { name: "新账户", markets: [1, 2], join: "exist", existingLogicalId: "la-existing" });
  // 打开对话框会使进行中的提交失效，等监听跑完再提交
  await flushPromises();
  return vm;
}

beforeEach(() => {
  mocks.createLogicalAccount.mockReset().mockResolvedValue({ logical_account: { logical_account_id: "la-made" } });
  mocks.createPaperSimulation.mockReset().mockResolvedValue({ account: { trading_account_id: "ta-new" } });
  mocks.addLogicalAccountMember.mockReset().mockResolvedValue({});
});
afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("新建账户提交", () => {
  it("模拟盘加入已有组合：先只建执行账户，再按现有成员之后的优先级加入", async () => {
    const vm = await openDialog();
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledWith(
      expect.objectContaining({ logical_account_name: "", markets: [1, 2] })
    );
    expect(mocks.addLogicalAccountMember).toHaveBeenCalledWith(
      expect.objectContaining({ logical_account_id: "la-existing", trading_account_id: "ta-new", priority: 3 })
    );
    expect(wrapper.emitted("created")).toEqual([["paper"]]);
  });

  it("加入组合失败后重试只补加入，不重复建账户", async () => {
    const vm = await openDialog();
    mocks.addLogicalAccountMember.mockRejectedValueOnce(new Error("先暂停自动执行"));
    await vm.submit();
    await flushPromises();
    expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toContain("先暂停自动执行");
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(1);
    expect(mocks.addLogicalAccountMember).toHaveBeenCalledTimes(2);
    expect(mocks.addLogicalAccountMember.mock.calls[1][0]).toMatchObject({ trading_account_id: "ta-new" });
  });

  it("新建组合时一次请求就完成，不再单独加入", async () => {
    const vm = await openDialog();
    Object.assign(vm.form, { join: "new", logicalName: "新组合" });
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledWith(expect.objectContaining({ logical_account_name: "新组合" }));
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
  });

  it("新账户的会话还没建起来时自动等待重试，不让用户手动再点", async () => {
    vi.useFakeTimers();
    try {
      const vm = await openDialog();
      mocks.addLogicalAccountMember
        .mockRejectedValueOnce(new Error("trade runtime: ExchangeSession is not configured"))
        .mockRejectedValueOnce(new Error("trade runtime: ExchangeSession is not configured"));
      const submitting = vm.submit();
      await vi.advanceTimersByTimeAsync(2600);
      expect(document.body.textContent).toContain("正在等待账户会话建立");
      await vi.advanceTimersByTimeAsync(2600);
      await submitting;
      expect(mocks.addLogicalAccountMember).toHaveBeenCalledTimes(3);
      expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(1);
      expect(wrapper.emitted("created")).toEqual([["paper"]]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("部分失败后改选新建组合，按账户类型建组合（模拟盘是 1，不是实盘），并锁定前面几步", async () => {
    const vm = await openDialog();
    mocks.addLogicalAccountMember.mockRejectedValueOnce(new Error("先暂停自动执行"));
    await vm.submit();
    await flushPromises();
    expect(wrapper.text() + document.body.textContent).toContain("不能再改");
    Object.assign(vm.form, { join: "new", logicalName: "补建的组合", controlMode: 2 });
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(1);
    expect(mocks.createLogicalAccount).toHaveBeenCalledWith(
      expect.objectContaining({ name: "补建的组合", execution_mode: 1, control_mode: 2 })
    );
    expect(mocks.addLogicalAccountMember).toHaveBeenLastCalledWith(
      expect.objectContaining({ logical_account_id: "la-made", trading_account_id: "ta-new", priority: 1 })
    );
  });

  it("实盘账户新建组合时执行模式是 2", async () => {
    mocks.createTradingAccount.mockResolvedValue({ account: { trading_account_id: "ta-live" } });
    const vm = await openDialog();
    // 1A 期间实盘选项在界面上不开放，这里直接验证提交逻辑
    Object.assign(vm.form, { kind: "testnet", secretId: "s1", join: "new", logicalName: "实盘组合" });
    await vm.submit();
    expect(mocks.createLogicalAccount).toHaveBeenCalledWith(expect.objectContaining({ execution_mode: 2 }));
  });
});
