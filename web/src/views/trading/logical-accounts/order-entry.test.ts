import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LogicalAccount, OperatorAction, Order, PlaceManualOrderReq } from "@/api/trade";

const mocks = vi.hoisted(() => ({
  listLogicalAccounts: vi.fn(),
  getLogicalAccount: vi.fn(),
  getLogicalAccountTarget: vi.fn(),
  getExecutionCapabilities: vi.fn(),
  submitOrder: vi.fn(),
  placeManualOrder: vi.fn(),
  getOperatorAction: vi.fn(),
  getOrder: vi.fn(),
  replace: vi.fn()
}));
vi.mock("@/api/trade", async () => ({
  ...(await vi.importActual<typeof import("@/api/trade")>("@/api/trade")),
  ...mocks
}));
vi.mock("vue-router", () => ({
  useRouter: () => ({ replace: mocks.replace }),
  useRoute: () => ({ query: { logical_account_id: "logical" } })
}));
vi.mock("./equity-curve.vue", () => ({ default: { template: "<div />" } }));
import { TradeResponseError } from "@/api/trade";
import LogicalAccounts from "./index.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

interface EntryVM {
  manualForm: PlaceManualOrderReq & { deadline_at: string };
  manualVisible: boolean;
  action: OperatorAction | null;
  submittedOrder: Order | null;
  selected: LogicalAccount;
  stateFilter: string;
  filteredRows: LogicalAccount[];
  rows: LogicalAccount[];
  startActionPolling(id: string): void;
  openManual(id: string): void;
  submitManual(): Promise<boolean>;
}
let wrapper: VueWrapper;
let logical: LogicalAccount;
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
async function openEntry() {
  wrapper = mount(LogicalAccounts, {
    global: { plugins: [ArcoVue], stubs: { EquityCurve: true, "icon-refresh": true, "icon-plus": true } }
  });
  await flushPromises();
  const vm = wrapper.vm as unknown as EntryVM;
  vm.openManual("account");
  await flushPromises();
  Object.assign(vm.manualForm, {
    action_id: "action",
    client_order_id: "client",
    instrument_id: "btc-usdt-spot",
    quantity: "0.01",
    reason: "test",
    deadline_at: "2030-01-01T12:00"
  });
  return vm;
}
beforeEach(() => {
  vi.useFakeTimers();
  logical = {
    logical_account_id: "logical",
    name: "Paper",
    control_mode: 2,
    market_type: 1,
    execution_mode: 1,
    automation_state: "PAUSED",
    ready: true,
    members: [{ trading_account_id: "account", enabled: true, priority: 0 }]
  } as LogicalAccount;
  mocks.listLogicalAccounts
    .mockReset()
    .mockImplementation(async () => ({ logical_accounts: [logical], page_result: { total: 1 } }));
  mocks.getLogicalAccount.mockReset().mockImplementation(async () => ({ logical_account: logical }));
  mocks.getLogicalAccountTarget.mockReset().mockResolvedValue({});
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

describe("trade order entry", () => {
  it("routes MANUAL to ordinary submission with an absolute deadline, never takeover", async () => {
    const vm = await openEntry();
    expect(document.body.textContent).not.toContain("提交前会暂停整个组合账户");
    expect(await vm.submitManual()).toBe(true);
    expect(mocks.submitOrder).toHaveBeenCalledWith(
      expect.objectContaining({
        logical_account_id: "logical",
        action_id: "action",
        client_order_id: "client",
        deadline_at: String(new Date("2030-01-01T12:00").getTime())
      })
    );
    expect(mocks.placeManualOrder).not.toHaveBeenCalled();
    expect(vm.submittedOrder?.state).toBe("PENDING");
  });
  it("keeps STRATEGY takeover explicit and sends NET for swaps", async () => {
    logical.control_mode = 1;
    logical.market_type = 2;
    const vm = await openEntry();
    expect(document.body.textContent).toContain("接管并下单");
    await vm.submitManual();
    expect(mocks.placeManualOrder).toHaveBeenCalledWith(expect.objectContaining({ position_side: 1 }));
    expect(mocks.submitOrder).not.toHaveBeenCalled();
  });
  it("retains durable identity on a nonzero response and polls through transient query errors", async () => {
    const vm = await openEntry();
    mocks.submitOrder.mockRejectedValueOnce(
      new TradeResponseError({ ...accepted(), ret_info: { code: 9, msg: "price timeout" } })
    );
    mocks.getOperatorAction.mockRejectedValueOnce(new Error("query timeout"));
    expect(await vm.submitManual()).toBe(false);
    expect(vm.action?.action_id).toBe("action");
    await vi.advanceTimersByTimeAsync(3100);
    expect(mocks.getOperatorAction.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(vm.action?.status).toBe("RUNNING");
  });
  it("freezes the original request across transport uncertainty and reopening the dialog", async () => {
    const vm = await openEntry();
    mocks.submitOrder.mockRejectedValueOnce(new Error("network timeout"));
    expect(await vm.submitManual()).toBe(false);
    const first = mocks.submitOrder.mock.calls[0][0];
    vm.manualVisible = false;
    vm.openManual("account");
    await flushPromises();
    expect(vm.manualForm.action_id).toBe("action");
    expect(vm.manualForm.client_order_id).toBe("client");
    await vm.submitManual();
    expect(mocks.submitOrder.mock.calls[1][0]).toEqual(first);
  });
  it("keeps querying an OPEN order after the action submission stage is completed", async () => {
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
    expect(vm.submittedOrder?.state).toBe("FILLED");
    await vi.advanceTimersByTimeAsync(3100);
    expect(mocks.getOrder).toHaveBeenCalledTimes(2);
  });
  it("does not attach a previous manual order to a flatten action", async () => {
    const vm = await openEntry();
    await vm.submitManual();
    mocks.getOperatorAction.mockResolvedValue({
      action: { action_id: "flatten", action_type: "FLATTEN", status: "COMPLETED", result_json: '{"accounts":[]}' }
    });
    vm.startActionPolling("flatten");
    await vi.advanceTimersByTimeAsync(1600);
    expect(mocks.getOrder).not.toHaveBeenCalled();
    expect(vm.submittedOrder).toBeNull();
  });
  it.each([1, 2, 3, 5, 14, "INVALID_PARAM", "CONFLICT"])(
    "leaves a definitively rejected request editable without polling (%s)",
    async code => {
      const vm = await openEntry();
      mocks.submitOrder.mockRejectedValueOnce(new TradeResponseError({ ret_info: { code, msg: "request rejected" } }));
      expect(await vm.submitManual()).toBe(false);
      await vi.advanceTimersByTimeAsync(1600);
      expect(mocks.getOperatorAction).not.toHaveBeenCalled();
      vm.manualForm.quantity = "0.02";
      await vm.submitManual();
      expect(mocks.submitOrder.mock.calls[1][0].quantity).toBe("0.02");
      expect(mocks.submitOrder.mock.calls[1][0].action_id).toBe("action");
    }
  );
  it("does not apply a late failed response to another account drawer", async () => {
    const vm = await openEntry();
    let reject!: (error: unknown) => void;
    mocks.submitOrder.mockImplementationOnce(
      () =>
        new Promise((_resolve, fail) => {
          reject = fail;
        })
    );
    const submitting = vm.submitManual();
    vm.selected = { ...logical, logical_account_id: "another" };
    reject(new TradeResponseError({ ...accepted(), ret_info: { code: 4, msg: "late timeout" } }));
    await submitting;
    expect(vm.action).toBeNull();
    await vi.advanceTimersByTimeAsync(1600);
    expect(mocks.getOperatorAction).not.toHaveBeenCalled();
  });
  it("does not include MANUAL accounts in the paused automation filter", async () => {
    const vm = await openEntry();
    vm.stateFilter = "PAUSED";
    expect(vm.filteredRows).toEqual([]);
  });
  it("refreshes both the drawer and list after takeover is accepted", async () => {
    logical.control_mode = 1;
    logical.automation_state = "ACTIVE";
    const vm = await openEntry();
    mocks.getLogicalAccount.mockResolvedValue({ logical_account: { ...logical, automation_state: "PAUSED" } });
    await vm.submitManual();
    await flushPromises();
    expect(vm.selected.automation_state).toBe("PAUSED");
    expect(vm.rows[0].automation_state).toBe("PAUSED");
  });
  it("tracks a RUNNING takeover that has no child order yet", async () => {
    logical.control_mode = 1;
    const vm = await openEntry();
    const deferredAction = { ...accepted().action, action_type: "MANUAL_ORDER", result_json: "{}" };
    mocks.placeManualOrder.mockResolvedValue({ ret_info: { code: 0 }, action: deferredAction });
    mocks.getOperatorAction.mockResolvedValue({ action: deferredAction });
    await vm.submitManual();
    await vi.advanceTimersByTimeAsync(1600);
    expect(vm.action?.status).toBe("RUNNING");
    expect(vm.submittedOrder).toBeNull();
    expect(mocks.getOrder).not.toHaveBeenCalled();
    expect(mocks.getOperatorAction).toHaveBeenCalledWith("action");
  });
  it.each([1, 5, 14])(
    "allows correcting a definitively rejected takeover without polling an unrelated action (%s)",
    async code => {
      logical.control_mode = 1;
      const vm = await openEntry();
      mocks.placeManualOrder.mockRejectedValueOnce(new TradeResponseError({ ret_info: { code, msg: "takeover rejected" } }));
      expect(await vm.submitManual()).toBe(false);
      await vi.advanceTimersByTimeAsync(1600);
      expect(mocks.getOperatorAction).not.toHaveBeenCalled();
      vm.manualForm.quantity = "0.02";
      await vm.submitManual();
      expect(mocks.placeManualOrder.mock.calls[1][0].quantity).toBe("0.02");
      expect(mocks.placeManualOrder.mock.calls[1][0].action_id).toBe("action");
    }
  );
});
