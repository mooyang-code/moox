import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("strategy create defaults", () => {
  it("uses a registry-valid declarative strategy DSL", () => {
    const source = fs.readFileSync(path.resolve(process.cwd(), "src/views/strategy/overview/index.vue"), "utf8");
    expect(source).toContain("name: momentum_demo");
    expect(source).toContain("triggers:");
    expect(source).toContain("data: {bar: 1h, calendar: crypto_24x7}");
    expect(source).toContain("rules:");
    expect(source).toContain("pool: {udf: spot_symbols}");
    expect(source).toContain("select: {top: 10}");
    expect(source).not.toContain("api_version:");
    expect(source).not.toContain("manifest_yaml");
  });
});
