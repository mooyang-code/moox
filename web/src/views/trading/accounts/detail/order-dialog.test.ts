import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent, h, ref } from "vue";
import type { LogicalAccount, OperatorAction, Order, TradingAccount } from "@/api/trade";

const mocks = vi.hoisted(() => ({
  getExecutionCapabilities: vi.fn(),
  listTradableInstruments: vi.fn(),
  submitOrder: vi.fn(),
  placeManualOrder: vi.fn(),
  getOperatorAction: vi.fn(),
  getOrder: vi.fn()
}));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
import { TradeResponseError } from "@/api/trade";
import { useActionTracker, type ActionTracker } from "./action-tracker";
import OrderDialog from "./order-dialog.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface DialogVM {
  manualForm: Record<string, unknown>;
  visible: boolean;
  openManual(accountId: string, instrumentId?: string): void;
  submitManual(): Promise<boolean>;
}

let wrapper: VueWrapper;
let tracker: ActionTracker;
let dialog: DialogVM;
let logicalRef = ref<LogicalAccount>();
let submitted = 0;

const accepted = () => ({
  ret_info: { code: 0, msg: "" },
  action: {
    action_id: "action",
    logical_account_id: "logical",
    action_type: "SUBMIT_ORDER",
    status: "RUNNING",
    result_json: '{"order_id":"order"}'
  } as OperatorAction,
  order: { order_id: "order", state: "PENDING" } as Order
});

function makeLogical(extra: Partial<LogicalAccount> = {}) {
  return {
    logical_account_id: "logical",
    name: "Paper",
    control_mode: 2,
    execution_mode: 1,
    markets: [1],
    automation_state: "PAUSED",
    ready: true,
    members: [{ trading_account_id: "account", enabled: true, priority: 0 }],
    ...extra
  } as LogicalAccount;
}

async function openEntry(env: "paper" | "prod" = "paper", instrumentId = "BTC-USDT-SPOT") {
  const accounts = new Map([["account", { trading_account_id: "account", name: "A", markets: [1, 2] } as TradingAccount]]);
  const Harness = defineComponent({
    setup(_, { expose }) {
      tracker = useActionTracker({ logicalAccountId: () => logicalRef.value?.logical_account_id, onChanged: () => undefined });
      const inner = ref<DialogVM>();
      expose({ inner });
      return () =>
        h(OrderDialog as never, {
          ref: inner,
          logical: logicalRef.value,
          accounts,
          env,
          tracker,
          onSubmitted: () => (submitted += 1)
        });
    }
  });
  wrapper = mount(Harness, { global: { plugins: [ArcoVue] } });
  await flushPromises();
  dialog = (wrapper.vm as unknown as { inner: DialogVM }).inner;
  dialog.openManual("account");
  await flushPromises();
  Object.assign(dialog.manualForm, {
    action_id: "action",
    client_order_id: "client",
    instrument_id: instrumentId,
    quantity: "0.01",
    reason: "test",
    deadline_at: "2030-01-01T12:00"
  });
  return dialog;
}

beforeEach(() => {
  vi.useFakeTimers();
  submitted = 0;
  logicalRef = ref(makeLogical());
  mocks.listTradableInstruments.mockReset().mockResolvedValue({ instruments: [] });
  mocks.getExecutionCapabilities
    .mockReset()
    .mockResolvedValue({ capabilities: { can_place_order: true, order_types: [1, 2], fill_policies: [1, 2, 3] } });
  mocks.submitOrder.mockReset().mockResolvedValue(accepted());
  mocks.placeManualOrder.mockReset().mockResolvedValue(accepted());
  mocks.getOperatorAction.mockReset().mockResolvedValue({ action: accepted().action });
  mocks.getOrder.mockReset().mockResolvedValue({ order: accepted().order });
});
afterEach(() => {
  wrapper?.unmount();
  vi.useRealTimers();
  document.body.innerHTML = "";
});

describe("下单对话框", () => {
  it("自主下单走普通提交并带绝对截止时间，不接管；请求带标的所在市场", async () => {
    const vm = await openEntry();
    expect(document.body.textContent).not.toContain("提交前会暂停整个组合账户");
    expect(await vm.submitManual()).toBe(true);
    expect(mocks.submitOrder).toHaveBeenCalledWith(
      expect.objectContaining({
        logical_account_id: "logical",
        action_id: "action",
        client_order_id: "client",
        market_type: 1,
        position_side: undefined,
        deadline_at: String(new Date("2030-01-01T12:00").getTime())
      })
    );
    expect(mocks.placeManualOrder).not.toHaveBeenCalled();
    expect(tracker.submittedOrder.value?.state).toBe("PENDING");
    expect(submitted).toBe(1);
  });

  it("策略驱动的接管下单要明说，永续订单带市场和净持仓方向，现货不带", async () => {
    logicalRef.value = makeLogical({ control_mode: 1, markets: [1, 2] });
    const vm = await openEntry("paper", "BTC-USDT-SWAP");
    expect(document.body.textContent).toContain("接管并下单");
    await vm.submitManual();
    expect(mocks.placeManualOrder).toHaveBeenCalledWith(expect.objectContaining({ market_type: 2, position_side: 1 }));
    expect(mocks.submitOrder).not.toHaveBeenCalled();
  });

  it("响应码非零时保留操作编号，查询暂时失败也继续轮询", async () => {
    const vm = await openEntry();
    mocks.submitOrder.mockRejectedValueOnce(
      new TradeResponseError({ ...accepted(), ret_info: { code: 9, msg: "price timeout" } })
    );
    mocks.getOperatorAction.mockRejectedValueOnce(new Error("query timeout"));
    expect(await vm.submitManual()).toBe(false);
    expect(tracker.action.value?.action_id).toBe("action");
    await vi.advanceTimersByTimeAsync(3100);
    expect(mocks.getOperatorAction.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(tracker.action.value?.status).toBe("RUNNING");
  });

  it("传输状态不明时冻结原请求，重新打开对话框后原样重试", async () => {
    const vm = await openEntry();
    mocks.submitOrder.mockRejectedValueOnce(new Error("network timeout"));
    expect(await vm.submitManual()).toBe(false);
    const first = mocks.submitOrder.mock.calls[0][0];
    vm.visible = false;
    vm.openManual("account");
    await flushPromises();
    expect(vm.manualForm.action_id).toBe("action");
    expect(vm.manualForm.client_order_id).toBe("client");
    await vm.submitManual();
    expect(mocks.submitOrder.mock.calls[1][0]).toEqual(first);
  });

  it("动作提交阶段完成后继续查订单直到结束", async () => {
    const vm = await openEntry();
    const completed = { ...accepted().action, status: "COMPLETED" };
    mocks.submitOrder.mockResolvedValueOnce({ ...accepted(), action: completed, order: { order_id: "order", state: "OPEN" } });
    mocks.getOperatorAction.mockResolvedValue({ action: completed });
    mocks.getOrder
      .mockResolvedValueOnce({ order: { order_id: "order", state: "OPEN" } })
      .mockResolvedValue({ order: { order_id: "order", state: "FILLED" } });
    await vm.submitManual();
    await vi.advanceTimersByTimeAsync(3100);
    expect(mocks.getOrder).toHaveBeenCalledTimes(2);
    expect(tracker.submittedOrder.value?.state).toBe("FILLED");
    await vi.advanceTimersByTimeAsync(3100);
    expect(mocks.getOrder).toHaveBeenCalledTimes(2);
  });

  it("不会把上一笔人工订单挂到清仓动作上", async () => {
    const vm = await openEntry();
    await vm.submitManual();
    mocks.getOperatorAction.mockResolvedValue({
      action: { action_id: "flatten", action_type: "FLATTEN", status: "COMPLETED", result_json: '{"accounts":[]}' }
    });
    tracker.start("flatten");
    await vi.advanceTimersByTimeAsync(1600);
    expect(mocks.getOrder).not.toHaveBeenCalled();
    expect(tracker.submittedOrder.value).toBeNull();
  });

  it.each([1, 2, 3, 5, 14, "INVALID_PARAM", "CONFLICT"])("明确被拒绝的请求可以修改后重填，不轮询（%s）", async code => {
    const vm = await openEntry();
    mocks.submitOrder.mockRejectedValueOnce(new TradeResponseError({ ret_info: { code, msg: "request rejected" } }));
    expect(await vm.submitManual()).toBe(false);
    await vi.advanceTimersByTimeAsync(1600);
    expect(mocks.getOperatorAction).not.toHaveBeenCalled();
    vm.manualForm.quantity = "0.02";
    await vm.submitManual();
    expect(mocks.submitOrder.mock.calls[1][0].quantity).toBe("0.02");
    expect(mocks.submitOrder.mock.calls[1][0].action_id).toBe("action");
  });

  it("切换到另一个组合后，迟到的失败响应不再影响页面", async () => {
    const vm = await openEntry();
    let reject!: (error: unknown) => void;
    mocks.submitOrder.mockImplementationOnce(
      () =>
        new Promise((_resolve, fail) => {
          reject = fail;
        })
    );
    const submitting = vm.submitManual();
    logicalRef.value = makeLogical({ logical_account_id: "another" });
    reject(new TradeResponseError({ ...accepted(), ret_info: { code: 4, msg: "late timeout" } }));
    await submitting;
    expect(tracker.action.value).toBeNull();
    await vi.advanceTimersByTimeAsync(1600);
    expect(mocks.getOperatorAction).not.toHaveBeenCalled();
  });

  it("策略驱动的接管下单还没有子订单时继续跟踪操作", async () => {
    logicalRef.value = makeLogical({ control_mode: 1 });
    const vm = await openEntry();
    const deferredAction = { ...accepted().action, action_type: "MANUAL_ORDER", result_json: "{}" };
    mocks.placeManualOrder.mockResolvedValue({ ret_info: { code: 0 }, action: deferredAction });
    mocks.getOperatorAction.mockResolvedValue({ action: deferredAction });
    await vm.submitManual();
    await vi.advanceTimersByTimeAsync(1600);
    expect(tracker.action.value?.status).toBe("RUNNING");
    expect(tracker.submittedOrder.value).toBeNull();
    expect(mocks.getOrder).not.toHaveBeenCalled();
    expect(mocks.getOperatorAction).toHaveBeenCalledWith("action");
  });

  it("生产环境要输入组合名称才能提交", async () => {
    await openEntry("prod");
    expect(document.body.textContent).toContain("输入组合名称「Paper」确认");
    expect(document.body.textContent).toContain("真实资金");
  });

  it("行内下单预填方向和数量", async () => {
    const accounts = new Map([["account", { trading_account_id: "account", name: "A", markets: [1, 2] } as TradingAccount]]);
    const Harness = defineComponent({
      setup(_, { expose }) {
        tracker = useActionTracker({ logicalAccountId: () => logicalRef.value?.logical_account_id, onChanged: () => undefined });
        const inner = ref<DialogVM & { open(instrumentId?: string, accountId?: string, prefill?: object): void }>();
        expose({ inner });
        return () => h(OrderDialog as never, { ref: inner, logical: logicalRef.value, accounts, env: "paper", tracker });
      }
    });
    wrapper = mount(Harness, { global: { plugins: [ArcoVue] } });
    await flushPromises();
    const vm = (
      wrapper.vm as unknown as {
        inner: { open: (a?: string, b?: string, c?: object) => void; manualForm: Record<string, unknown> };
      }
    ).inner;
    vm.open("BTC-USDT-SWAP", "account", { side: 2, quantity: "0.6" });
    await flushPromises();
    expect(vm.manualForm).toMatchObject({ instrument_id: "BTC-USDT-SWAP", side: 2, quantity: "0.6" });
  });
});
