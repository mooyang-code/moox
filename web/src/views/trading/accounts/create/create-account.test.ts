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
  it("模拟盘加入已有组合：一次请求完成，后端在同一个事务里加入，前端不再单独加成员", async () => {
    const vm = await openDialog();
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(1);
    expect(mocks.createPaperSimulation).toHaveBeenCalledWith(
      expect.objectContaining({ logical_account_id: "la-existing", logical_account_name: "", markets: [1, 2] })
    );
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
    expect(wrapper.emitted("created")).toEqual([["paper"]]);
  });

  it("模拟盘加入失败时后端不留账户：不锁定前几步，再点一次重新提交整个请求", async () => {
    const vm = await openDialog();
    mocks.createPaperSimulation.mockRejectedValueOnce(new Error("组合账户正在自动执行，先暂停再加入新账户"));
    await vm.submit();
    await flushPromises();
    expect(document.body.textContent).toContain("先暂停再加入新账户");
    expect(document.body.textContent).not.toContain("已锁定");
    expect(wrapper.emitted("created")).toBeUndefined();
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledTimes(2);
  });

  it("新建组合时一次请求就完成", async () => {
    const vm = await openDialog();
    Object.assign(vm.form, { join: "new", logicalName: "新组合" });
    await vm.submit();
    expect(mocks.createPaperSimulation).toHaveBeenCalledWith(
      expect.objectContaining({ logical_account_name: "新组合", logical_account_id: undefined })
    );
    expect(mocks.addLogicalAccountMember).not.toHaveBeenCalled();
  });

  it("提交到一半对话框被关闭重开：账户已建成仍通知列表刷新，不动新打开的对话框", async () => {
    const vm = await openDialog();
    let resolve!: (value: unknown) => void;
    mocks.createPaperSimulation.mockImplementationOnce(() => new Promise(done => (resolve = done)));
    const submitting = vm.submit();
    // 取消后又重新打开：关闭会使进行中的提交失效
    vm.visible = false;
    await flushPromises();
    vm.open("paper");
    await flushPromises();
    resolve({ account: { trading_account_id: "ta-late" } });
    await submitting;
    expect(wrapper.emitted("created")).toEqual([["paper"]]);
    expect(vm.visible).toBe(true);
  });

  describe("实盘路径（1A 期间界面不开放，这里直接验证提交逻辑）", () => {
    const liveForm = { kind: "testnet", secretId: "s1", name: "实盘账户", markets: [2] };

    it("加入组合失败只显示一条红色提示并锁定前几步，重试只补加入不重复建账户", async () => {
      mocks.createTradingAccount.mockResolvedValue({ account: { trading_account_id: "ta-live" } });
      const vm = await openDialog();
      Object.assign(vm.form, liveForm);
      mocks.addLogicalAccountMember.mockRejectedValueOnce(new Error("先暂停自动执行"));
      await vm.submit();
      await flushPromises();
      const text = document.body.textContent || "";
      expect(text).toContain("先暂停自动执行");
      expect(text).toContain("已锁定");
      expect(document.querySelectorAll(".arco-alert").length).toBe(1);
      expect(wrapper.emitted("created")).toEqual([["testnet"]]);
      await vm.submit();
      expect(mocks.createTradingAccount).toHaveBeenCalledTimes(1);
      expect(mocks.addLogicalAccountMember).toHaveBeenCalledTimes(2);
    });

    it("新建组合的执行模式是 2；组合建好后组合名称、运行方式置灰，重试不再重复建组合", async () => {
      mocks.createTradingAccount.mockResolvedValue({ account: { trading_account_id: "ta-live" } });
      const vm = await openDialog();
      Object.assign(vm.form, { ...liveForm, join: "new", logicalName: "实盘组合", step: 3 });
      mocks.addLogicalAccountMember.mockRejectedValueOnce(new Error("暂时失败"));
      await vm.submit();
      await flushPromises();
      expect(mocks.createLogicalAccount).toHaveBeenCalledWith(expect.objectContaining({ execution_mode: 2 }));
      // 组合名称输入框、运行方式和“新建/加入”的单选都置灰
      const disabled = [...document.querySelectorAll<HTMLInputElement>(".arco-modal input")].filter(item => item.disabled);
      expect(disabled.length).toBeGreaterThanOrEqual(3);
      await vm.submit();
      expect(mocks.createLogicalAccount).toHaveBeenCalledTimes(1);
      expect(mocks.addLogicalAccountMember).toHaveBeenLastCalledWith(
        expect.objectContaining({ logical_account_id: "la-made", trading_account_id: "ta-live" })
      );
    });
  });
});
