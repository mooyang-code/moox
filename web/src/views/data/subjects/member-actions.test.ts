import { describe, expect, it, vi } from "vitest";

const arco = vi.hoisted(() => ({
  confirm: vi.fn(),
  success: vi.fn()
}));
vi.mock("@arco-design/web-vue", () => ({ Modal: { confirm: arco.confirm }, Message: { success: arco.success } }));
import { confirmRemoveMembers, removeMembersConfirmText } from "./member-actions";

type ConfirmOptions = { onBeforeOk: () => Promise<boolean> };
const lastOptions = () => arco.confirm.mock.calls.at(-1)![0] as ConfirmOptions;

describe("移出标签成员", () => {
  it("确认说明写出数量和对采集任务的影响", () => {
    expect(removeMembersConfirmText(3)).toBe("将移出 3 个对象。引用这个标签的采集任务下一周期起不再采集它们。");
  });

  it("只弹确认框，不点“移出”（取消或关掉）不发请求", () => {
    const remove = vi.fn(async () => undefined);
    const done = vi.fn();
    confirmRemoveMembers(2, remove, done);
    expect(arco.confirm).toHaveBeenCalledTimes(1);
    expect(remove).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
  });

  it("点“移出”才发请求；失败时确认框不关、不刷新", async () => {
    const remove = vi.fn(async () => {
      throw new Error("服务不可用");
    });
    const done = vi.fn();
    confirmRemoveMembers(2, remove, done);
    await expect(lastOptions().onBeforeOk()).resolves.toBe(false);
    expect(remove).toHaveBeenCalledTimes(1);
    expect(done).not.toHaveBeenCalled();

    remove.mockResolvedValueOnce(undefined);
    confirmRemoveMembers(2, remove, done);
    await expect(lastOptions().onBeforeOk()).resolves.toBe(true);
    expect(done).toHaveBeenCalledTimes(1);
  });
});
