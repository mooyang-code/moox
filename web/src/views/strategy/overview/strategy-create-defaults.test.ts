import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("strategy create defaults", () => {
  it("uses a registry-valid manifest and the three-argument Python entrypoint", () => {
    const source = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/overview/index.vue"), "utf8");
    expect(source).toContain("api_version: moox.strategy/v1");
    expect(source).toContain("entrypoint: strategy.py:run");
    expect(source).toContain("history_bars: 200");
    expect(source).toContain("def run(context, data, params):");
    expect(source).not.toContain("def strategy(data, params, context):");
  });
});
