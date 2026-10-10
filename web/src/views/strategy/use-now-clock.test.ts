import { mount } from "@vue/test-utils";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computed, defineComponent, h } from "vue";
import { deriveTargetState } from "@/views/strategy/model";
import { useNowClock } from "@/views/strategy/use-now-clock";

const instance = { enabled: true, session_id: "s1" };
const snapshot = {
  session_id: "s1",
  bar_end_time: "2026-10-01T01:00:00Z",
  valid_until: "2026-10-01T03:00:00Z",
  targets: [{ instrument_id: "BTC-USDT", target_weight: "0.5" }]
} as never;

describe("useNowClock", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-01T02:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("lets a derived target state turn expired as time passes without any refresh", async () => {
    const wrapper = mount(
      defineComponent({
        setup() {
          const nowMs = useNowClock(1000);
          const state = computed(() => deriveTargetState(instance, snapshot, nowMs.value));
          return () => h("span", state.value);
        }
      })
    );
    expect(wrapper.text()).toBe("valid");
    vi.setSystemTime(new Date("2026-10-01T03:00:01Z"));
    await vi.advanceTimersByTimeAsync(1000);
    expect(wrapper.text()).toBe("expired");
    wrapper.unmount();
  });

  it("stops ticking after unmount", async () => {
    const wrapper = mount(defineComponent({ setup: () => (useNowClock(1000), () => h("span")) }));
    wrapper.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
