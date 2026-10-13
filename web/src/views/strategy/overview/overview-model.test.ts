import { describe, expect, it } from "vitest";
import type { Replay, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import {
  CARD_CHIP_LIMIT,
  activeReplays,
  instanceCards,
  overviewStats,
  pendingItems,
  stateOf,
  type OverviewInput
} from "./overview-model";

const NOW = Date.parse("2026-10-10T12:00:00Z");

function instance(id: string, overrides: Partial<StrategyInstance> = {}): StrategyInstance {
  return {
    instance_id: id,
    strategy_id: "s1",
    space_id: "crypto",
    view_id: "v",
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
function input(overrides: Partial<OverviewInput> = {}): OverviewInput {
  return { instances: [], targets: {}, replays: [], catalog: [], nowMs: NOW, ...overrides };
}

describe("strategy overview model", () => {
  it("derives the target state, treating unread enabled instances as unknown", () => {
    const targets = { a: snapshot("2026-10-10T13:00:00Z"), b: snapshot("2026-10-10T11:30:00Z") };
    expect(stateOf(instance("a"), targets, NOW)).toBe("valid");
    expect(stateOf(instance("b"), targets, NOW)).toBe("expired");
    expect(stateOf(instance("c"), targets, NOW)).toBe("unknown");
    expect(stateOf(instance("d", { enabled: false }), targets, NOW)).toBe("inactive");
  });

  it("summarizes instances, valid targets, attention items and running replays", () => {
    const stats = overviewStats(
      input({
        instances: [instance("a"), instance("b", { health: "degraded" }), instance("c", { enabled: false, session_id: "" })],
        targets: { a: snapshot("2026-10-10T13:00:00Z"), b: snapshot("2026-10-10T13:00:00Z") },
        replays: [{ status: "running" }, { status: "done" }, { status: "pending" }] as Replay[]
      })
    );
    expect(Object.fromEntries(stats.map(item => [item.key, item.value]))).toEqual({
      instances: "2 / 3",
      valid: "2 / 2",
      attention: "1",
      replays: "2"
    });
    expect(stats.find(item => item.key === "attention")?.tone).toBe("danger");
  });

  it("puts the largest weights on the card and folds the rest", () => {
    const weights = Object.fromEntries(Array.from({ length: 8 }, (_, i) => [`I${i}`, String((i + 1) / 100)]));
    weights.SHORT = "-0.5";
    const [card] = instanceCards(
      input({
        instances: [instance("a")],
        targets: { a: snapshot("2026-10-10T13:00:00Z", weights) },
        catalog: [{ strategy_id: "s1", name: "动量" }] as never
      })
    );
    expect(card.title).toBe("动量");
    expect(card.chips).toHaveLength(CARD_CHIP_LIMIT);
    expect(card.chips[0]).toEqual({ instrumentId: "SHORT", text: "SHORT -50.00%", short: true });
    expect(card.more).toBe(9 - CARD_CHIP_LIMIT);
    expect(card.stateTag.label).toBe("目标有效");
    expect(card.links.results).toEqual({ name: "strategy-tasks", query: { tab: "results", instance: "a" } });
  });

  it("lists one pending item per instance by severity", () => {
    const items = pendingItems(
      input({
        instances: [
          instance("deg", { health: "degraded" }),
          instance("unv", { health: "session_unverified" }),
          instance("exp"),
          instance("rel", { enabled: false, session_id: "s" }),
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
      ["unv", "session_unverified"],
      ["exp", "expired"],
      ["rel", "releasing"]
    ]);
  });

  it("keeps only pending and running replays", () => {
    expect(activeReplays([{ status: "pending" }, { status: "failed" }, { status: "running" }] as Replay[])).toHaveLength(2);
  });
});
