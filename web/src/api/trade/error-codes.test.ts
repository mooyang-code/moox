import { describe, expect, it } from "vitest";
import { RequestError } from "@/api/http";
import { isTradeAdoptionRequired, isTradeNotFound, isTradeSessionNotReady } from "@/api/trade";

describe("交易服务的专门错误码", () => {
  it("会话未启动、需要接管、不存在按错误码识别，数字和枚举名都认", () => {
    const error = (code: number | string) => new RequestError("", { ret_info: { code, msg: "" } });
    expect(isTradeSessionNotReady(error(19))).toBe(true);
    expect(isTradeSessionNotReady(error("TRADE_SESSION_NOT_READY"))).toBe(true);
    expect(isTradeSessionNotReady(error(4))).toBe(false);
    expect(isTradeAdoptionRequired(error(20))).toBe(true);
    expect(isTradeAdoptionRequired(error("TRADE_ADOPTION_REQUIRED"))).toBe(true);
    expect(isTradeNotFound(error("NOT_FOUND"))).toBe(true);
    expect(isTradeSessionNotReady(new Error("交易账户会话还没有启动"))).toBe(false);
  });
});
