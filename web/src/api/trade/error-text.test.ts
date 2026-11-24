import { describe, expect, it } from "vitest";
import { RequestError } from "@/api/http";
import { isTradeAdoptionRequired, isTradeNotFound, isTradeSessionNotReady } from "@/api/trade";
import { tradeErrorText } from "./error-text";

const text = (msg: string, code: number | string = 1) => tradeErrorText({ code, msg });

describe("交易服务错误的展示文字", () => {
  it("没有消息时给出错误码；已知的英文错误换成中文，未知的原样返回", () => {
    expect(text("", 13)).toBe("交易服务请求失败（错误码 13）");
    expect(text("trade: invalid trading account")).toBe("执行账户的参数不合法");
    expect(text("执行账户上已有持仓或挂单")).toBe("执行账户上已有持仓或挂单");
  });

  it("包装进去的具体原因保留在括号里；冲突类不接原文；只认完整的错误常量", () => {
    expect(text("trade store: conflict: stale paper order")).toBe("数据已经被修改，刷新后再试");
    expect(text("trade store: conflict")).toBe("数据已经被修改，刷新后再试");
    expect(text("trade: invalid order transition")).toBe("trade: invalid order transition");
    expect(text("trade: invalid order: quantity must be positive")).toBe("订单参数不合法（quantity must be positive）");
    expect(text("trade: invalid order specification")).toBe("订单参数不合法");
  });

  it("网关或交易服务超时的英文原文换成一句中文", () => {
    expect(text("tcp client transport ReadFrame: read tcp 10.1.12.13:37552->43.132.204.177:11003: i/o timeout")).toBe(
      "交易服务响应超时，请稍后重试"
    );
    expect(text("交易服务内部错误：context deadline exceeded")).toBe("交易服务响应超时，请稍后重试");
  });

  it("“记录不合法”只是外层包装：后面接中文原因时只显示原因，英文原因放在括号里；全角冒号同样认得", () => {
    expect(text("trade store: invalid record: 修改组合成员前要先暂停组合")).toBe("修改组合成员前要先暂停组合");
    expect(text("trade store: invalid record")).toBe("数据不合法");
    expect(text("trade store: invalid record: missing logical account")).toBe("数据不合法（missing logical account）");
    expect(text("trade store: invalid record：bad state")).toBe("数据不合法（bad state）");
    expect(text("trade: invalid order：数量必须大于 0")).toBe("订单参数不合法（数量必须大于 0）");
    expect(text("trade store: conflict：")).toBe("数据已经被修改，刷新后再试");
  });

  it("名称重复、唯一约束冲突的固定文案原样显示", () => {
    const taken = "名称已被使用，请换一个（已关闭的模拟账户也占用名称）";
    expect(text(taken, "INVALID_PARAM")).toBe(taken);
    expect(text("trade store: conflict: stale order version", "CONFLICT")).toBe("数据已经被修改，刷新后再试");
  });
});

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
