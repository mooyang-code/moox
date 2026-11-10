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

  it("操作查到了但订单一直查不到时错误不清除，不会每轮闪一次", async () => {
    const { tracker, wrapper } = setup();
    mocks.getOperatorAction.mockResolvedValue({
      action: { ...running.action, action_type: "SUBMIT_ORDER", result_json: '{"order_id":"o1"}' }
    });
    mocks.getOrder.mockRejectedValue(new Error("订单查询失败"));
    tracker.start("a1");
    await vi.advanceTimersByTimeAsync(1500);
    expect(tracker.error.value).toBe("订单查询失败");
    await vi.advanceTimersByTimeAsync(3000);
    expect(tracker.error.value).toBe("订单查询失败");
    wrapper.unmount();
  });
});
