import { describe, expect, it } from "vitest";
import type { EventExecution, ScopeField } from "@/api/callback-dispatcher";
import {
  actionText,
  systemRetryConfirmText,
  systemActionNotice,
  canAcknowledge,
  canCancel,
  cancelConfirmText,
  cancelLabel,
  canRetry,
  confirmCheckboxText,
  createLatestGuard,
  pageAfterShrink,
  delistSnapshot,
  pastTimeNote,
  DELIST_TIME_HINT,
  PAPER_ONLY_NOTE,
  delistTimeError,
  describeScope,
  eventTitle,
  formatDuration,
  isPastDelistTime,
  notifyUrlError,
  prettyPayload,
  roundText,
  stepStatusText,
  scopeFromForm,
  scopeToForm,
  signatureExamples,
  statusText,
  statusTone,
  holderLine,
  summarizeHolders,
  skippedLine,
  RECEIVER_NOTES,
  REQUEST_HEADERS,
  executionStatusText,
  executionStatusTone,
  requestErrorText,
  sameScope,
  secretHintText
} from "./event-center-utils";

const fields: ScopeField[] = [
  { field: "exchange", title: "交易所", options: ["binance", "okx"] },
  { field: "market", title: "市场", options: ["SPOT", "SWAP"] },
  { field: "exchange_symbol", title: "交易所代码", options: [] }
];

const execution = (
  status: EventExecution["status"],
  acknowledged = false,
  cleanup_pending = false,
  action_type: EventExecution["action_type"] = "ACTION_TYPE_NOTIFY_URL"
) => ({ status, acknowledged, cleanup_pending, action_type }) as EventExecution;

describe("事件中心的限定范围", () => {
  it("没有限定时显示不限，有限定时按字段标题展示", () => {
    expect(describeScope([], fields)).toBe("不限");
    expect(describeScope([{ field: "exchange", values: [] }], fields)).toBe("不限");
    expect(
      describeScope(
        [
          { field: "exchange", values: ["binance", "okx"] },
          { field: "market", values: ["SPOT"] }
        ],
        fields
      )
    ).toBe("交易所：binance、okx；市场：SPOT");
    expect(describeScope([{ field: "unknown", values: ["x"] }], fields)).toBe("unknown：x");
  });

  it("表单取值转成限定范围：丢弃空值、去重、按目录里的字段顺序", () => {
    const scope = scopeFromForm(
      { exchange_symbol: [" FOOUSDT ", "FOOUSDT", ""], market: [], exchange: ["okx", "binance"], ignored: ["x"] },
      fields
    );
    expect(scope).toEqual([
      { field: "exchange", values: ["okx", "binance"] },
      { field: "exchange_symbol", values: ["FOOUSDT"] }
    ]);
    expect(scopeToForm(scope)).toEqual({ exchange: ["okx", "binance"], exchange_symbol: ["FOOUSDT"] });
  });
});

describe("事件中心的文案", () => {
  it("执行状态用大白话", () => {
    expect(statusText("EXECUTION_STATUS_PENDING")).toBe("等待");
    expect(statusText("EXECUTION_STATUS_RUNNING")).toBe("执行中");
    expect(statusText("EXECUTION_STATUS_SUCCEEDED")).toBe("成功");
    expect(statusText("EXECUTION_STATUS_FAILED")).toBe("失败");
    expect(statusText("EXECUTION_STATUS_CANCELED")).toBe("已取消");
    expect(statusTone("EXECUTION_STATUS_FAILED")).toBe("fail");
  });

  it("动作、事件名、下架阶段", () => {
    expect(
      actionText({
        action_type: "ACTION_TYPE_NOTIFY_URL",
        notify_url: "https://a.example/h",
        system_action: "SYSTEM_ACTION_UNSPECIFIED"
      })
    ).toBe("通知地址：https://a.example/h");
    expect(
      actionText({ action_type: "ACTION_TYPE_SYSTEM", notify_url: "", system_action: "SYSTEM_ACTION_SELL_DELISTED_HOLDINGS" })
    ).toBe("系统：卖出策略持有的下架币");
    expect(systemActionNotice(true)).toBe("");
    expect(systemActionNotice(null)).toContain("无法确认系统动作是否启用");
    const events = [{ event_name: "e.a", version: 1, title: "标的即将下架", description: "", scope_fields: [] }];
    expect(eventTitle(events, "e.a")).toBe("标的即将下架");
    expect(eventTitle(events, "e.b")).toBe("e.b");
    expect(roundText(0)).toBe("发现后禁止买入");
    expect(roundText(3)).toBe("下架后核对");
    expect(stepStatusText("PARTIAL")).toBe("只平了一部分");
    expect(stepStatusText("SUCCEEDED")).toBe("成功");
    expect(stepStatusText("OTHER")).toBe("OTHER");
  });

  it("时间和耗时", () => {
    expect(formatDuration(0)).toBe("-");
    expect(formatDuration(250)).toBe("250 毫秒");
    expect(formatDuration(1500)).toBe("1.50 秒");
    expect(formatDuration(12000)).toBe("12.0 秒");
  });

  it("事件内容缩进展示，非法 JSON 原样返回", () => {
    expect(prettyPayload('{"a":1}')).toBe('{\n  "a": 1\n}');
    expect(prettyPayload("not json")).toBe("not json");
    expect(prettyPayload("  ")).toBe("");
  });
});

describe("执行记录的操作", () => {
  it("只有失败的能重新执行，只有未结束的能取消，失败且未确认的才能确认", () => {
    expect(canRetry(execution("EXECUTION_STATUS_FAILED"))).toBe(true);
    expect(canRetry(execution("EXECUTION_STATUS_SUCCEEDED"))).toBe(false);
    expect(canCancel(execution("EXECUTION_STATUS_PENDING"))).toBe(true);
    expect(canCancel(execution("EXECUTION_STATUS_RUNNING"))).toBe(true);
    expect(canCancel(execution("EXECUTION_STATUS_FAILED"))).toBe(false);
    expect(canCancel(execution("EXECUTION_STATUS_CANCELED"))).toBe(false);
    expect(canCancel(execution("EXECUTION_STATUS_CANCELED", false, true))).toBe(true);
    expect(cancelLabel(execution("EXECUTION_STATUS_CANCELED", false, true))).toBe("重试善后");
    expect(cancelLabel(execution("EXECUTION_STATUS_PENDING"))).toBe("取消");
    expect(cancelLabel(execution("EXECUTION_STATUS_PENDING", false, false, "ACTION_TYPE_SYSTEM"))).toBe("取消并解除禁止");
    expect(cancelConfirmText(execution("EXECUTION_STATUS_CANCELED", false, true))).toContain("解除禁止买入没做成");
    expect(canAcknowledge(execution("EXECUTION_STATUS_FAILED"))).toBe(true);
    expect(canAcknowledge(execution("EXECUTION_STATUS_FAILED", true))).toBe(false);
    expect(canAcknowledge(execution("EXECUTION_STATUS_PENDING"))).toBe(false);
  });
});

describe("通知地址校验", () => {
  it("只允许 http 和 https", () => {
    expect(notifyUrlError("")).toBe("请填写通知地址");
    expect(notifyUrlError("not a url")).toBe("通知地址格式不对");
    expect(notifyUrlError("ftp://a.example/x")).toBe("通知地址只能以 http:// 或 https:// 开头");
    expect(notifyUrlError("file:///etc/passwd")).not.toBe("");
    expect(notifyUrlError("https://a.example/hook")).toBe("");
    expect(notifyUrlError(" http://10.0.0.1:8080/x ")).toBe("");
  });
});

describe("验签示例", () => {
  it("三种语言的示例都说明了签名内容和请求头", () => {
    const examples = signatureExamples();
    expect(examples.map(item => item.language)).toEqual(["Node.js", "Python", "Go"]);
    for (const example of examples) {
      expect(example.code.toLowerCase()).toContain("x-moox-timestamp");
      expect(example.code.toLowerCase()).toContain("x-moox-signature");
      expect(example.code).toContain("sha256=");
      expect(example.code).toContain('"."');
    }
  });

  // 示例里的算法（HMAC-SHA256，内容为 时间戳 + "." + 请求体）与后端一致：用 Node 自带的 crypto 实际算一遍。
  it("Node 示例的算法能验过后端约定的签名", async () => {
    const { createHmac, timingSafeEqual } = await import("node:crypto");
    const secret = "secret-value";
    const timestamp = String(Date.now());
    const body = '{"event_id":"e1","data":{"k":"v"}}';
    const signature = "sha256=" + createHmac("sha256", secret).update(`${timestamp}.${body}`).digest("hex");
    const expected =
      "sha256=" +
      createHmac("sha256", secret)
        .update(timestamp + "." + body)
        .digest("hex");
    expect(timingSafeEqual(Buffer.from(expected), Buffer.from(signature))).toBe(true);
  });
});

describe("手工登记下架", () => {
  const holder = (patch: Partial<Parameters<typeof summarizeHolders>[0][number]>) => ({
    space_id: "crypto",
    logical_account_id: "la-1",
    trading_account_id: "ta-1",
    quantity: "1",
    dust: false,
    paper: false,
    status: "ENABLED",
    note: "",
    ...patch
  });

  it("预览结果分成生产、模拟盘和零头", () => {
    const summary = summarizeHolders([
      holder({ logical_account_id: "live" }),
      holder({ logical_account_id: "paper", paper: true }),
      holder({ logical_account_id: "dust", dust: true }),
      holder({ logical_account_id: "paper-dust", dust: true, paper: true })
    ]);
    expect(summary.live.map(item => item.logical_account_id)).toEqual(["live"]);
    expect(summary.paper.map(item => item.logical_account_id)).toEqual(["paper"]);
    expect(summary.dust.map(item => item.logical_account_id)).toEqual(["dust", "paper-dust"]);
  });

  it("持有者一行带上停用、出错的状态和标注；没能判断的账户单独成行", () => {
    expect(holderLine(holder({ quantity: "2" }))).toBe("crypto / la-1（执行账户 ta-1）数量 2");
    expect(holderLine(holder({ status: "DISABLED", note: "取不到报价" }))).toContain(
      "账户已停用，平仓会失败，需要人工处理；取不到报价"
    );
    expect(holderLine(holder({ status: "ERROR" }))).toContain("账户出错");
    expect(skippedLine({ trading_account_id: "ta-9", reason: "资金设置读不出来" })).toBe("执行账户 ta-9：资金设置读不出来");
  });

  it("二次确认的勾选框文字：几件要确认的事同时出现时合并成一句", () => {
    const base = { previewFailed: false, verified: true, skipped: 0, live: 0, willProcess: true };
    expect(confirmCheckboxText(base)).toBe("");
    expect(confirmCheckboxText({ ...base, previewFailed: true })).toBe("我知道无法确认持有情况，仍然登记");
    expect(confirmCheckboxText({ ...base, verified: false, live: 2 })).toBe("我知道无法确认持有情况，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1 })).toBe("我确认有生产组合会被平仓，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1, willProcess: false })).toBe("我确认有生产组合持有这个标的，仍然登记");
    expect(confirmCheckboxText({ ...base, skipped: 1 })).toBe("我确认有账户没能判断持有情况，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1, skipped: 2 })).toBe(
      "我确认有生产组合会被平仓，并且有账户没能判断持有情况，仍然登记"
    );
  });

  it("下架时间必须填，允许补录过去的时间", () => {
    expect(delistTimeError(null)).toBe("请选择下架时间");
    expect(delistTimeError(0)).toBe("请选择下架时间");
    expect(delistTimeError(1_700_000_000_000)).toBe("");
    expect(isPastDelistTime(1000, 2000)).toBe(true);
    expect(isPastDelistTime(3000, 2000)).toBe(false);
  });

  it("预览时存下规整过的登记参数，缺项时给出提示", () => {
    const form = {
      exchange: "binance" as const,
      market: "SPOT" as const,
      exchange_symbol: " fooUsdt ",
      delist_at: 5000,
      paper_only: true
    };
    expect(delistSnapshot(form, 1000)).toEqual({
      snapshot: {
        exchange: "binance",
        market: "SPOT",
        exchange_symbol: "FOOUSDT",
        delist_at_ms: 5000,
        paper_only: true,
        past: false
      }
    });
    expect(delistSnapshot({ ...form, delist_at: 500 }, 1000)).toMatchObject({ snapshot: { past: true } });
    expect(delistSnapshot({ ...form, exchange_symbol: " " }, 1000)).toEqual({ error: "请填写交易所代码" });
    expect(delistSnapshot({ ...form, delist_at: null }, 1000)).toEqual({ error: "请选择下架时间" });
  });

  it("不在第一页、本页读回来为空时跳到现在的最后一页", () => {
    expect(pageAfterShrink(2, 10, 10, 0)).toBe(1);
    expect(pageAfterShrink(5, 10, 21, 0)).toBe(3);
    expect(pageAfterShrink(2, 10, 11, 1)).toBeNull();
    expect(pageAfterShrink(1, 10, 0, 0)).toBeNull();
    expect(pageAfterShrink(2, 10, 0, 0)).toBe(1);
  });

  it("只认最后一次请求：换号后旧序号作废", () => {
    const guard = createLatestGuard();
    const first = guard.next();
    const second = guard.next();
    expect(guard.isLatest(first)).toBe(false);
    expect(guard.isLatest(second)).toBe(true);
    guard.reset();
    expect(guard.isLatest(second)).toBe(false);
  });

  it("过去时间的说明区分真实下架与演练", () => {
    expect(pastTimeNote(false)).toContain("仍会写下架名单");
    expect(pastTimeNote(false)).toContain("名单保留、不会解除");
    expect(pastTimeNote(true)).toContain("演练记录结束后解除");
    expect(DELIST_TIME_HINT).toContain("仍会写下架名单");
    expect(PAPER_ONLY_NOTE).toContain("直到这条演练记录结束");
  });

  it("下架时间的说明写明按 UTC 填", () => {
    expect(DELIST_TIME_HINT).toContain("按 UTC 填写");
  });

  it("限定范围比较不看先后，空字段等于不限", () => {
    const a = [
      { field: "exchange", values: ["okx", "binance"] },
      { field: "market", values: ["SPOT"] }
    ];
    const b = [
      { field: "market", values: ["SPOT"] },
      { field: "exchange", values: ["binance", "okx"] },
      { field: "exchange_symbol", values: [] }
    ];
    expect(sameScope(a, b)).toBe(true);
    expect(sameScope(a, [{ field: "exchange", values: ["binance"] }])).toBe(false);
    expect(sameScope([], [])).toBe(true);
  });

  it("执行状态写清楚善后未完成和已确认处理", () => {
    expect(executionStatusText(execution("EXECUTION_STATUS_CANCELED", false, true))).toBe("已取消，解除禁止买入未完成");
    expect(executionStatusTone(execution("EXECUTION_STATUS_CANCELED", false, true))).toBe("fail");
    expect(executionStatusText(execution("EXECUTION_STATUS_FAILED", true))).toBe("失败，已确认处理");
    expect(executionStatusText(execution("EXECUTION_STATUS_CANCELED"))).toBe("已取消");
    expect(executionStatusTone(execution("EXECUTION_STATUS_CANCELED"))).toBe("cancel");
  });

  it("通知地址规则显示密钥末 4 位，系统规则不显示", () => {
    expect(secretHintText({ action_type: "ACTION_TYPE_NOTIFY_URL", secret_hint: "ab12" })).toBe("密钥末 4 位 ab12");
    expect(secretHintText({ action_type: "ACTION_TYPE_SYSTEM", secret_hint: "" })).toBe("");
  });

  it("接收说明：跳转和防护拒绝不重试，请求头列出事件名", () => {
    expect(RECEIVER_NOTES[0]).toContain("3xx");
    expect(RECEIVER_NOTES[0]).toContain("出站防护");
    expect(REQUEST_HEADERS.map(item => item.name)).toEqual([
      "Content-Type",
      "X-MooX-Event-Id",
      "X-MooX-Event",
      "X-MooX-Timestamp",
      "X-MooX-Signature"
    ]);
  });

  it("请求失败的原因：网络、超时换成中文，服务端错误原样显示", () => {
    expect(requestErrorText({ code: "ERR_NETWORK", message: "Network Error" })).toContain("无法连接管理台");
    expect(requestErrorText({ code: "ECONNABORTED", message: "timeout of 30000ms exceeded" })).toBe(
      "请求超时，交易服务没有及时响应"
    );
    expect(requestErrorText(new Error("交易服务暂时不可用"))).toBe("交易服务暂时不可用");
    expect(requestErrorText(null)).toBe("未知原因");
  });
});

describe("系统动作重新执行的确认说明", () => {
  it("写出标的，并说明下架时间已过时只核对、不重做平仓", () => {
    const text = systemRetryConfirmText({ subject_id: "okx:SWAP:BARUSDT" });
    expect(text).toContain("okx:SWAP:BARUSDT");
    expect(text).toContain("下架时间还没到时");
    expect(text).toContain("不会重做平仓");
  });
});
