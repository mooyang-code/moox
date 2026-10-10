import { describe, expect, it } from "vitest";
import { serviceHealth } from "./service-health";
describe("home component health", () => {
  it("counts healthy host-component placements rather than enabled registry rows", () => {
    const result = serviceHealth({
      topology_known: true,
      summary: {
        healthy_count: 500,
        components: { healthy_count: 3, attention_count: 1, unknown_count: 1, unchecked_count: 1, disabled_count: 2 }
      }
    });
    expect(result.healthy).toBe(3);
    expect(result.total).toBe(6);
    expect(result.percent).toBe(50);
    expect(result.tone).toBe("danger");
  });
  it("never shows green or invented numbers after a failed or incomplete observation", () => {
    const overview = { topology_known: true, summary: { components: { healthy_count: 3 } } };
    expect(serviceHealth(overview).tone).toBe("ok");
    expect(serviceHealth(overview, "offline").healthy).toBeUndefined();
    expect(serviceHealth({ ...overview, topology_known: false }).percent).toBeUndefined();
    expect(serviceHealth().healthy).toBeUndefined();
    expect(serviceHealth({ topology_known: true, summary: { components: {} } }).tone).toBe("neutral");
    expect(serviceHealth({ topology_known: true, summary: { components: { healthy_count: 3, unknown_count: 1 } } }).tone).toBe(
      "neutral"
    );
  });
});
