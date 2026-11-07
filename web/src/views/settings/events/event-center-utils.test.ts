import { describe, expect, it } from "vitest";
import type { EventExecution, ScopeField } from "@/api/callback-dispatcher";
import {
  actionText,
  SYSTEM_ACTION_DISABLED_TEXT,
  canAcknowledge,
  canCancel,
  cancelConfirmText,
  cancelLabel,
  canRetry,
  delistTimeError,
  describeScope,
  eventTitle,
  formatDuration,
  formatMs,
  isPastDelistTime,
  pickerValueToMs,
  notifyUrlError,
  prettyPayload,
  roundText,
  scopeFromForm,
  scopeToForm,
  signatureExamples,
  statusText,
  statusTone,
  summarizeHolders
} from "./event-center-utils";

const fields: ScopeField[] = [
  { field: "exchange", title: "交易所", options: ["binance", "okx"] },
  { field: "market", title: "市场", options: ["SPOT", "SWAP"] },
  { field: "exchange_symbol", title: "交易所代码", options: [] }
];

const execution = (status: EventExecution["status"], acknowledged = false, cleanup_pending = false) =>
  ({ status, acknowledged, cleanup_pending }) as EventExecution;

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
    expect(
      actionText(
        { action_type: "ACTION_TYPE_SYSTEM", notify_url: "", system_action: "SYSTEM_ACTION_SELL_DELISTED_HOLDINGS" },
        false
      )
    ).toBe("系统：卖出策略持有的下架币（尚未启用，只记录）");
    expect(SYSTEM_ACTION_DISABLED_TEXT).toContain("系统动作尚未启用，现在只记录");
    const events = [{ event_name: "e.a", version: 1, title: "标的即将下架", description: "", scope_fields: [] }];
    expect(eventTitle(events, "e.a")).toBe("标的即将下架");
    expect(eventTitle(events, "e.b")).toBe("e.b");
    expect(roundText(0)).toBe("发现后禁止买入");
    expect(roundText(3)).toBe("下架后核对");
  });

  it("时间和耗时", () => {
    expect(formatMs(0)).toBe("-");
    expect(formatMs(new Date(2026, 10, 4, 8, 5, 9).getTime())).toBe("2026-11-04 08:05:09");
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

  it("下架时间必须填，允许补录过去的时间", () => {
    expect(delistTimeError(null)).toBe("请选择下架时间");
    expect(delistTimeError(0)).toBe("请选择下架时间");
    expect(delistTimeError(1_700_000_000_000)).toBe("");
    expect(isPastDelistTime(1000, 2000)).toBe(true);
    expect(isPastDelistTime(3000, 2000)).toBe(false);
  });

  it("日期选择器按时间戳取值，兼容字符串，没选时是 0", () => {
    expect(pickerValueToMs(1_700_000_000_000)).toBe(1_700_000_000_000);
    expect(pickerValueToMs("2026-11-07T08:00:00Z")).toBe(Date.parse("2026-11-07T08:00:00Z"));
    expect(pickerValueToMs("")).toBe(0);
    expect(pickerValueToMs(null)).toBe(0);
    expect(pickerValueToMs("不是时间")).toBe(0);
  });
});
