import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TradeResponseError, type LogicalAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({
  createPaperSimulation: vi.fn(),
  createTradingAccount: vi.fn(),
  createLogicalAccount: vi.fn(),
  addLogicalAccountMember: vi.fn(),
  inspectExchangeCredential: vi.fn()
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
  mocks.createTradingAccount.mockReset();
  mocks.addLogicalAccountMember.mockReset().mockResolvedValue({});
  mocks.inspectExchangeCredential.mockReset();
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

  describe("实盘路径", () => {
    function nextButton() {
      return [...document.querySelectorAll<HTMLButtonElement>(".arco-modal button")].find(item =>
        item.textContent?.includes("下一步")
      );
    }

    it("选完密钥检测通过才放行“下一步”，检测没有结论或有阻断项都不放行", async () => {
      const vm = await openDialog();
      vm.open("testnet");
      Object.assign(vm.form, { step: 1, name: "实盘账户", secretId: "s1" });
      mocks.inspectExchangeCredential.mockRejectedValueOnce(new Error("请求超时"));
      await flushPromises();
      await vi.waitFor(() => expect(document.body.textContent).toContain("这次没有检测成功"));
      expect(nextButton()?.disabled).toBe(true);

      mocks.inspectExchangeCredential.mockResolvedValueOnce({
        inspection: { account_mode: "BINANCE_STANDARD", blockers: [{ code: "WITHDRAW", message: "密钥有提现权限" }] }
      });
      Object.assign(vm.form, { secretId: "s2" });
      await flushPromises();
      await vi.waitFor(() => expect(document.body.textContent).toContain("密钥有提现权限"));
      expect(nextButton()?.disabled).toBe(true);

      mocks.inspectExchangeCredential.mockResolvedValueOnce({
        inspection: { account_mode: "BINANCE_STANDARD", available_markets: [1, 2] }
      });
      Object.assign(vm.form, { secretId: "s3" });
      await flushPromises();
      await vi.waitFor(() => expect(nextButton()?.disabled).toBe(false));
    });

    const liveForm = { kind: "testnet", secretId: "s1", name: "实盘账户", markets: [2] };

    it("第三步按所选市场再检测一次：和市场有关的阻断项在这里拦，第二步只拦和市场无关的", async () => {
      const vm = await openDialog();
      vm.open("prod");
      Object.assign(vm.form, { step: 1, name: "实盘账户", secretId: "s1", markets: [1, 2] });
      mocks.inspectExchangeCredential.mockResolvedValueOnce({
        inspection: { account_mode: "BINANCE_STANDARD", available_markets: [1, 2] }
      });
      await flushPromises();
      await vi.waitFor(() => expect(nextButton()?.disabled).toBe(false));
      expect(mocks.inspectExchangeCredential.mock.calls[0][0].markets).toBeUndefined();

      // 进入第三步：按所选市场再检测，合约双向持仓这类和市场有关的阻断项在这里拦下。
      mocks.inspectExchangeCredential.mockResolvedValueOnce({
        inspection: { account_mode: "BINANCE_STANDARD", blockers: [{ code: "hedge_position_mode", message: "合约是双向持仓" }] }
      });
      vm.form.step = 2;
      await flushPromises();
      await vi.waitFor(() => expect(document.body.textContent).toContain("合约是双向持仓"));
      expect(mocks.inspectExchangeCredential.mock.calls[1][0].markets).toEqual([1, 2]);
      expect(nextButton()?.disabled).toBe(true);
    });

    it("币安测试网一个账户只能开通一个市场：勾另一个市场是切换，不是追加", async () => {
      const vm = await openDialog();
      vm.open("testnet");
      Object.assign(vm.form, { step: 2, name: "测试网账户", secretId: "s1", markets: [2] });
      await flushPromises();
      const spot = [...document.querySelectorAll<HTMLElement>(".arco-checkbox")].find(item => item.textContent?.includes("现货"));
      spot!.querySelector("input")!.click();
      await flushPromises();
      expect(vm.form.markets).toEqual([1]);
    });

    it("币安测试网单选：取消勾选照常生效；已勾两个市场时换成测试网会收成一个", async () => {
      const vm = await openDialog();
      vm.open("testnet");
      Object.assign(vm.form, { step: 2, name: "测试网账户", secretId: "s1", markets: [2] });
      await flushPromises();
      const checkbox = (label: string) =>
        [...document.querySelectorAll<HTMLElement>(".arco-checkbox")]
          .find(item => item.textContent?.includes(label))!
          .querySelector("input")!;
      checkbox("永续").click();
      await flushPromises();
      expect(vm.form.markets, "取消勾选生效").toEqual([]);

      // OKX 测试网可以勾两个市场；换成币安后收成一个，之后能正常取消。
      Object.assign(vm.form, { exchange: 2, markets: [1, 2] });
      await flushPromises();
      expect(vm.form.markets).toEqual([1, 2]);
      vm.form.exchange = 1;
      await flushPromises();
      expect(vm.form.markets).toEqual([1]);
      checkbox("现货").click();
      await flushPromises();
      expect(vm.form.markets).toEqual([]);
    });

    it("收成一个市场后默认费率跟着重算：OKX 测试网勾两个市场后换成币安测试网，费率按现货默认", async () => {
      const vm = await openDialog();
      vm.open("testnet");
      Object.assign(vm.form, { step: 2, name: "测试网账户", secretId: "s1", exchange: 2, markets: [2] });
      await flushPromises();
      const checkbox = (label: string) =>
        [...document.querySelectorAll<HTMLElement>(".arco-checkbox")]
          .find(item => item.textContent?.includes(label))!
          .querySelector("input")!;
      checkbox("现货").click();
      await flushPromises();
      expect(vm.form.markets).toEqual([1, 2]);
      expect([vm.form.makerFeeRate, vm.form.takerFeeRate]).toEqual(["0.0002", "0.0005"]);

      vm.form.exchange = 1;
      await flushPromises();
      expect(vm.form.markets).toEqual([1]);
      expect([vm.form.makerFeeRate, vm.form.takerFeeRate], "按现货的默认费率").toEqual(["0.001", "0.001"]);

      // 手工改过费率的不覆盖。
      Object.assign(vm.form, { exchange: 2, markets: [1, 2], makerFeeRate: "0.0003", feesEdited: true });
      await flushPromises();
      vm.form.exchange = 1;
      await flushPromises();
      expect(vm.form.makerFeeRate).toBe("0.0003");
    });

    it("换密钥后，“确认这是生产环境密钥且没有提现权限”作废", async () => {
      const vm = await openDialog();
      vm.open("prod");
      Object.assign(vm.form, { step: 2, secretId: "s1", ack: true });
      await flushPromises();
      expect(vm.form.ack).toBe(false);
      vm.form.ack = true;
      vm.form.secretId = "s2";
      await flushPromises();
      expect(vm.form.ack).toBe(false);
    });

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

    it("后端返回需要接管（TRADE_ADOPTION_REQUIRED）时提示勾选后重试，勾上后重试带上接管", async () => {
      mocks.createTradingAccount.mockResolvedValue({ account: { trading_account_id: "ta-live" } });
      const vm = await openDialog();
      Object.assign(vm.form, { ...liveForm, join: "new", logicalName: "实盘组合", step: 3 });
      await flushPromises();
      expect(document.body.textContent).toContain("接管账户里已有的持仓");
      // 普通的参数不合法不再猜测是要接管
      mocks.addLogicalAccountMember.mockRejectedValueOnce(
        new TradeResponseError({ ret_info: { code: 1, msg: "组合成员的环境和结算资产必须一致" } })
      );
      await vm.submit();
      await flushPromises();
      expect(document.body.textContent).not.toContain("后再点一次。");
      mocks.addLogicalAccountMember.mockRejectedValueOnce(
        new TradeResponseError({
          ret_info: { code: 20, msg: "执行账户上已有持仓或挂单，加入组合需要确认接管这些已有仓位" }
        })
      );
      await vm.submit();
      await flushPromises();
      expect(mocks.addLogicalAccountMember).toHaveBeenLastCalledWith(expect.objectContaining({ adopt_existing_exposure: false }));
      expect(document.body.textContent).toContain("勾选下方“接管账户里已有的持仓”后再点一次");
      vm.form.adoptExisting = true;
      await vm.submit();
      expect(mocks.createTradingAccount).toHaveBeenCalledTimes(1);
      expect(mocks.addLogicalAccountMember).toHaveBeenLastCalledWith(
        expect.objectContaining({ logical_account_id: "la-made", adopt_existing_exposure: true })
      );
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
