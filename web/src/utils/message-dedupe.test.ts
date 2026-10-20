import { beforeEach, describe, expect, it, vi } from "vitest";
import { Message } from "@arco-design/web-vue";
import { dedupeMessageConfig, installMessageDedupe } from "./message-dedupe";

const calls: unknown[] = [];
vi.mock("@arco-design/web-vue", () => ({
  Message: {
    error: (config: unknown) => calls.push(config),
    warning: (config: unknown) => calls.push(config),
    success: (config: unknown) => calls.push(config)
  }
}));

describe("相同文案的提示只显示一条", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("字符串提示按文案生成稳定的 id", () => {
    expect(dedupeMessageConfig("Network Error")).toEqual({ content: "Network Error", id: "dedupe:Network Error" });
    expect(dedupeMessageConfig("Network Error")).toEqual(dedupeMessageConfig("Network Error"));
    expect(dedupeMessageConfig("另一条")).not.toEqual(dedupeMessageConfig("Network Error"));
  });

  it("对象配置没有 id 时补上，已有 id 时保持不变，其它选项保留", () => {
    expect(dedupeMessageConfig({ content: "出错了", duration: 5000 })).toEqual({
      content: "出错了",
      duration: 5000,
      id: "dedupe:出错了"
    });
    expect(dedupeMessageConfig({ content: "出错了", id: "mine" })).toEqual({ content: "出错了", id: "mine" });
    const vnodeContent = { content: () => "组件内容" };
    expect(dedupeMessageConfig(vnodeContent)).toBe(vnodeContent);
  });

  it("安装后 error 与 warning 都带上 id，success 不受影响", () => {
    installMessageDedupe();
    Message.error("Network Error");
    Message.error("Network Error");
    Message.warning({ content: "磁盘快满了" });
    Message.success("保存成功");
    expect(calls).toEqual([
      { content: "Network Error", id: "dedupe:Network Error" },
      { content: "Network Error", id: "dedupe:Network Error" },
      { content: "磁盘快满了", id: "dedupe:磁盘快满了" },
      "保存成功"
    ]);
  });
});
