import { mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { defineComponent } from "vue";

const mocks = vi.hoisted(() => ({ getOperatorAction: vi.fn(), getOrder: vi.fn() }));
vi.mock("@/api/trade", () => mocks);
import { useActionTracker } from "./action-tracker";

function setup() {
  let tracker!: ReturnType<typeof useActionTracker>;
  const wrapper = mount(
    defineComponent({
      setup() {
        tracker = useActionTracker({ logicalAccountId: () => "la-1", onChanged: vi.fn() });
        return () => null;
      }
    })
  );
  return { tracker, wrapper };
}

const running = { action: { action_id: "a1", action_type: "CLOSE_ALL", status: "RUNNING" } };

beforeEach(() => {
  vi.useFakeTimers();
  mocks.getOperatorAction.mockReset();
  mocks.getOrder.mockReset();
});
afterEach(() => vi.useRealTimers());

describe("操作跟踪", () => {
  it("查询失败显示错误，下一轮查询成功后错误清除", async () => {
    const { tracker, wrapper } = setup();
    mocks.getOperatorAction.mockRejectedValueOnce(new Error("网络中断")).mockResolvedValue(running);
    tracker.start("a1");
    await vi.advanceTimersByTimeAsync(1500);
    expect(tracker.error.value).toBe("网络中断");
    await vi.advanceTimersByTimeAsync(1500);
    expect(tracker.error.value).toBe("");
    expect(tracker.action.value?.action_id).toBe("a1");
    wrapper.unmount();
  });

  it("下一轮操作查到了、订单还没返回时，错误保持到订单也查成功才清除", async () => {
    const { tracker, wrapper } = setup();
    mocks.getOperatorAction.mockResolvedValue({
      action: { ...running.action, action_type: "SUBMIT_ORDER", result_json: '{"order_id":"o1"}' }
    });
    let resolveOrder!: (value: unknown) => void;
    mocks.getOrder
      .mockRejectedValueOnce(new Error("订单查询失败"))
      .mockImplementationOnce(() => new Promise(done => (resolveOrder = done)));
    tracker.start("a1");
    await vi.advanceTimersByTimeAsync(1500);
    expect(tracker.error.value).toBe("订单查询失败");
    // 第二轮：操作查到了，订单请求挂着；此时错误还在
    await vi.advanceTimersByTimeAsync(1500);
    expect(mocks.getOrder).toHaveBeenCalledTimes(2);
    expect(tracker.error.value).toBe("订单查询失败");
    resolveOrder({ order: { order_id: "o1", state: "OPEN" } });
    await vi.advanceTimersByTimeAsync(0);
    expect(tracker.error.value).toBe("");
    wrapper.unmount();
  });
});
