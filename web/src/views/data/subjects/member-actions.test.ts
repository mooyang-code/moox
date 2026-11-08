import { describe, expect, it } from "vitest";
import { removeMembersConfirmText } from "./member-actions";

describe("移出标签成员的确认说明", () => {
  it("写出数量和对采集任务的影响", () => {
    expect(removeMembersConfirmText(3)).toBe("将移出 3 个对象。引用这个标签的采集任务下一周期起不再采集它们。");
  });
});
