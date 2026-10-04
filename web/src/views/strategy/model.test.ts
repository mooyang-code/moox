import { describe, expect, it } from "vitest";
import { deriveTargetState, targetWeightPercent } from "./model";

describe("strategy target state", () => {
  const now = Date.parse("2026-09-06T01:30:00Z");
  it("distinguishes no evaluation from an explicit zero target", () => {
    expect(deriveTargetState({ enabled: true, session_id: "s1" }, { targets: [], session_id: "", bar_end_time: "", valid_until: "" }, now)).toBe("empty");
    expect(deriveTargetState({ enabled: true, session_id: "s1" }, { targets: [], session_id: "s1", bar_end_time: "2026-09-06T01:00:00Z", valid_until: "2026-09-06T03:00:00Z" }, now)).toBe("zero");
  });
  it("never presents disabled or mismatched sessions as active", () => {
    const snapshot = { targets: [{ instrument_id: "BTC", target_weight: "0.6" }], session_id: "s1", bar_end_time: "2026-09-06T01:00:00Z", valid_until: "2026-09-06T03:00:00Z" };
    expect(deriveTargetState({ enabled: false, session_id: "s1" }, snapshot, now)).toBe("inactive");
    expect(deriveTargetState({ enabled: true, session_id: "s2" }, snapshot, now)).toBe("unknown");
  });
  it("marks expired targets and formats signed weight values", () => {
    const snapshot = { targets: [{ instrument_id: "BTC", target_weight: "-0.125" }], session_id: "s1", bar_end_time: "2026-09-06T00:00:00Z", valid_until: "2026-09-06T01:00:00Z" };
    expect(deriveTargetState({ enabled: true, session_id: "s1" }, snapshot, now)).toBe("expired");
    expect(targetWeightPercent(snapshot.targets[0])).toBe("-12.50%");
  });
});
