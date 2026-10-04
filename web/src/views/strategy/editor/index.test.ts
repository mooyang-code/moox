import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/editor/index.vue"), "utf8");

describe("strategy DSL editor contract", () => {
  it("keeps create and update as definition-only operations", () => {
    expect(source).toContain("createStrategy({ strategy_id: strategyId.value.trim(), dsl_yaml: source.value })");
    expect(source).toContain("updateStrategy(strategyId.value.trim(), source.value)");
    expect(source).not.toContain("createInstance(");
    expect(source).not.toContain("enabled: true");
  });

  it("protects a dirty draft when leaving or replacing a template", () => {
    expect(source).toContain("当前 DSL 尚未保存，确认替换？");
    expect(source).toContain("当前 DSL 尚未保存，确认离开？");
    expect(source).toContain("onBeforeRouteLeave");
    expect(source).toContain("onBeforeRouteUpdate");
  });
});
