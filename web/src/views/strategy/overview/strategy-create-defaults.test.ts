import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("strategy create defaults", () => {
  it("uses a registry-valid declarative weight manifest", () => {
    const source = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/overview/index.vue"), "utf8");
    expect(source).toContain("api_version: moox.strategy/v2");
    expect(source).toContain("kind: coin_selection");
    expect(source).toContain("source_view_id:");
    expect(source).toContain("instrument_pool:");
    expect(source).toContain("side_weight:");
    expect(source).not.toContain("entrypoint:");
  });
});
