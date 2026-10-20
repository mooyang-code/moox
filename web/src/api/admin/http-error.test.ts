import { beforeEach, describe, expect, it, vi } from "vitest";
import { Message } from "@arco-design/web-vue";
import { reportControlError } from "./http";

vi.mock("@arco-design/web-vue", () => ({ Message: { error: vi.fn() } }));

describe("control error feedback", () => {
  beforeEach(() => vi.mocked(Message.error).mockClear());

  it("shows business errors that were not reported by the transport interceptor", () => {
    reportControlError(new Error("节点下仍有服务实例"));
    expect(Message.error).toHaveBeenCalledWith("节点下仍有服务实例");
  });

  it("does not show the same error twice", () => {
    const error = new Error("network unavailable");
    reportControlError(error);
    reportControlError(error);
    expect(Message.error).toHaveBeenCalledOnce();
  });
});
