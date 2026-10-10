import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/components/strategy-instance-create.vue"), "utf8");

describe("strategy instance creation contract", () => {
  it("binds a definition, one View and an optional account, and stays disabled", () => {
    expect(source).toContain("createInstance({");
    expect(source).toContain("view_id: form.view_id");
    expect(source).toContain('Message.success("策略实例已创建并保持停用")');
    expect(source).not.toContain("setInstanceEnabled(");
    expect(source).not.toContain("input_bindings_json");
    expect(source).not.toContain("buildInputBindings");
  });

  it("prechecks the binding through the server instead of rebuilding it locally", () => {
    expect(source).toContain("validateStrategy(selectedStrategy.value.dsl_yaml, form.view_id)");
    expect(source).toContain("ResolvedTable");
    expect(source).toContain("创建请求结果未知，但实例 ID 已存在");
  });
});
