import { describe, expect, it } from "vitest";
import type { Replay, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { activeReplays, pendingItems, stateOf, type AttentionInput } from "./attention-model";

const NOW = Date.parse("2026-10-10T12:00:00Z");

function instance(id: string, overrides: Partial<StrategyInstance> = {}): StrategyInstance {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    logical_account_id: "",
    enabled: true,
    session_id: "sess",
    resolved_json: "{}",
    health: "ok",
    created_at: "",
    updated_at: "2026-10-10T00:00:00Z",
    ...overrides
  };
}
function snapshot(validUntil: string, weights: Record<string, string> = { "BTC-USDT": "0.5" }): StrategyTargetSnapshot {
  return {
    targets: Object.entries(weights).map(([instrument_id, target_weight]) => ({ instrument_id, target_weight })),
    session_id: "sess",
    bar_end_time: "2026-10-10T11:00:00Z",
    valid_until: validUntil,
    result_id: "r"
  };
}
function input(overrides: Partial<AttentionInput> = {}): AttentionInput {
  return { instances: [], targets: {}, catalog: [], nowMs: NOW, ...overrides };
}

describe("策略的待处理与进行中的回放", () => {
  it("derives the target state, treating unread enabled instances as unknown", () => {
    const targets = { a: snapshot("2026-10-10T13:00:00Z"), b: snapshot("2026-10-10T11:30:00Z") };
    expect(stateOf(instance("a"), targets, NOW)).toBe("valid");
    expect(stateOf(instance("b"), targets, NOW)).toBe("expired");
    expect(stateOf(instance("c"), targets, NOW)).toBe("unknown");
    expect(stateOf(instance("d", { enabled: false }), targets, NOW)).toBe("inactive");
  });

  it("lists one pending item per instance by severity", () => {
    const items = pendingItems(
      input({
        instances: [
          instance("deg", { health: "degraded" }),
          instance("exp"),
          instance("off", { enabled: false }),
          instance("fine")
        ],
        targets: {
          exp: snapshot("2026-10-10T11:00:00Z"),
          fine: snapshot("2026-10-10T13:00:00Z"),
          deg: snapshot("2026-10-10T11:00:00Z")
        }
      })
    );
    expect(items.map(item => [item.instanceId, item.kind])).toEqual([
      ["deg", "degraded"],
      ["exp", "expired"]
    ]);
  });

  it("绑定的组合账户已暂停时列为待处理，链接到组合账户", () => {
    const items = pendingItems(
      input({
        instances: [instance("p", { logical_account_id: "acc1" }), instance("q", { logical_account_id: "acc2" })],
        targets: { p: snapshot("2026-10-10T13:00:00Z"), q: snapshot("2026-10-10T13:00:00Z") },
        pausedAccounts: new Set(["acc1"])
      })
    );
    expect(items.map(item => [item.instanceId, item.kind])).toEqual([["p", "paused"]]);
    expect(items[0].target).toEqual({ name: "trading-account-detail", params: { logical_account_id: "acc1" } });
  });

  it("keeps only pending and running replays", () => {
    expect(activeReplays([{ status: "pending" }, { status: "failed" }, { status: "running" }] as Replay[])).toHaveLength(2);
  });
});
