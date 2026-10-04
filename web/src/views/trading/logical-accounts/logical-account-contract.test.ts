import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

describe("组合账户子视图契约", () => {
  it("使用组合账户和执行账户中文概念并支持内嵌布局", () => {
    expect(source).toContain("defineProps<{ embedded?: boolean }>");
    expect(source).toContain('class="moox-page logical-page" :class="{ \'is-embedded\': embedded }"');
    expect(source).toContain("新建组合账户");
    expect(source).toContain("组合账户详情");
    expect(source).toContain("执行账户");
    expect(source).toContain("多个执行账户按优先级执行，当前账户容量不足时自动切换下一账户。");
    expect(source).not.toContain("策略账户");
    expect(source).not.toContain("实际交易账户");
    expect(source).not.toContain("<h2>逻辑账户</h2>");
    expect(source).not.toContain("物理交易账户");
  });

  it("保留组合账户执行控制和成员操作 API", () => {
    for (const call of [
      "pauseLogicalAccount",
      "resumeLogicalAccount",
      "flattenLogicalAccount",
      "placeManualOrder",
      "addLogicalAccountMember",
      "removeLogicalAccountMember"
    ]) {
      expect(source).toContain(call);
    }
    expect(source).toContain("selected?.automation_state !== 'PAUSED'");
  });
});
