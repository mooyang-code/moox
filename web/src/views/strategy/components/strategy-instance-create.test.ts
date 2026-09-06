import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(process.cwd(), "src/views/strategy/components/strategy-instance-create.vue"), "utf8");

describe("strategy instance creation contract", () => {
  it("creates a disabled instance and never claims the account locally", () => {
    expect(source).toContain("createInstance({");
    expect(source).toContain("Message.success(\"策略实例已创建并保持停用\")");
    expect(source).not.toContain("setInstanceEnabled(");
  });

  it("loads paginated metadata and checks the result binding", () => {
    expect(source).toContain("loadAllViews");
    expect(source).toContain("loadAllFactors");
    expect(source).toContain("loadAllBindings");
    expect(source).toContain("findOutputColumn");
    expect(source).toContain("每个因子都必须选择定义、绑定、输出和结果列");
    expect(source).toContain("创建请求结果未知，但实例 ID 已存在");
  });
});
