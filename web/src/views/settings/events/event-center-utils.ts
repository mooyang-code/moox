import type {
  DelistStepKind,
  EventExecution,
  EventRule,
  ExecutionStatus,
  ScopeField,
  ScopeFilter,
  SubscribableEvent
} from "@/api/callback-dispatcher";
import type { InstrumentHolder } from "@/api/callback-dispatcher/delist";

export type StatusTone = "wait" | "run" | "ok" | "fail" | "cancel";

const STATUS_TEXT: Record<ExecutionStatus, { text: string; tone: StatusTone }> = {
  EXECUTION_STATUS_UNSPECIFIED: { text: "未知", tone: "cancel" },
  EXECUTION_STATUS_PENDING: { text: "等待", tone: "wait" },
  EXECUTION_STATUS_RUNNING: { text: "执行中", tone: "run" },
  EXECUTION_STATUS_SUCCEEDED: { text: "成功", tone: "ok" },
  EXECUTION_STATUS_FAILED: { text: "失败", tone: "fail" },
  EXECUTION_STATUS_CANCELED: { text: "已取消", tone: "cancel" }
};

export const EXECUTION_STATUS_FILTERS: { value: ExecutionStatus; label: string }[] = (
  [
    "EXECUTION_STATUS_PENDING",
    "EXECUTION_STATUS_RUNNING",
    "EXECUTION_STATUS_SUCCEEDED",
    "EXECUTION_STATUS_FAILED",
    "EXECUTION_STATUS_CANCELED"
  ] as ExecutionStatus[]
).map(value => ({ value, label: STATUS_TEXT[value].text }));

export function statusText(status: ExecutionStatus): string {
  return STATUS_TEXT[status]?.text ?? "未知";
}

export function statusTone(status: ExecutionStatus): StatusTone {
  return STATUS_TEXT[status]?.tone ?? "cancel";
}

const STEP_KIND_TEXT: Record<DelistStepKind, string> = {
  DELIST_STEP_KIND_UNSPECIFIED: "未知",
  DELIST_STEP_KIND_BLOCK: "禁止买入",
  DELIST_STEP_KIND_FLATTEN: "平仓",
  DELIST_STEP_KIND_VERIFY: "核对"
};

export function stepKindText(kind: DelistStepKind): string {
  return STEP_KIND_TEXT[kind] ?? "未知";
}

/** 下架处理各轮的说明：第 0 轮发现即禁止买入，之后是下架前 24 小时平仓、2 小时复查、下架后核对。 */
export function roundText(round: number): string {
  switch (round) {
    case 0:
      return "发现后禁止买入";
    case 1:
      return "下架前 24 小时平仓";
    case 2:
      return "下架前 2 小时复查";
    case 3:
      return "下架后核对";
    default:
      return `第 ${round} 轮`;
  }
}

const pad = (value: number) => String(value).padStart(2, "0");

/** Unix 毫秒转本地时间 “YYYY-MM-DD HH:mm:ss”；没有值（0）显示 “-”。 */
export function formatMs(ms: number): string {
  if (!ms) return "-";
  const date = new Date(ms);
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export function formatDuration(ms: number): string {
  if (!ms) return "-";
  if (ms < 1000) return `${ms} 毫秒`;
  return `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)} 秒`;
}

export function eventTitle(events: SubscribableEvent[], eventName: string): string {
  return events.find(event => event.event_name === eventName)?.title || eventName;
}

/** 限定范围的展示：“交易所：binance、okx；市场：SPOT”；没有限定显示“不限”。 */
export function describeScope(scope: ScopeFilter[], fields: ScopeField[] = []): string {
  const parts = scope
    .filter(item => item.values.length > 0)
    .map(item => {
      const title = fields.find(field => field.field === item.field)?.title || item.field;
      return `${title}：${item.values.join("、")}`;
    });
  return parts.length ? parts.join("；") : "不限";
}

/** 表单里每个字段对应一组取值；取值为空的字段不进入限定范围，保持事件目录里的字段顺序。 */
export function scopeFromForm(values: Record<string, string[]>, fields: ScopeField[]): ScopeFilter[] {
  const scope: ScopeFilter[] = [];
  for (const field of fields) {
    const list = (values[field.field] ?? []).map(value => value.trim()).filter(Boolean);
    if (list.length) scope.push({ field: field.field, values: [...new Set(list)] });
  }
  return scope;
}

export function scopeToForm(scope: ScopeFilter[]): Record<string, string[]> {
  const values: Record<string, string[]> = {};
  for (const item of scope) values[item.field] = [...item.values];
  return values;
}

export function actionText(rule: Pick<EventRule, "action_type" | "notify_url" | "system_action">): string {
  if (rule.action_type === "ACTION_TYPE_SYSTEM") return "系统：卖出策略持有的下架币";
  return `通知地址：${rule.notify_url}`;
}

/** 通知地址只允许 http、https。返回错误说明，合法时返回空串。 */
export function notifyUrlError(raw: string): string {
  const text = raw.trim();
  if (!text) return "请填写通知地址";
  let url: URL;
  try {
    url = new URL(text);
  } catch {
    return "通知地址格式不对";
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return "通知地址只能以 http:// 或 https:// 开头";
  if (!url.hostname) return "通知地址缺少主机名";
  return "";
}

export const canRetry = (item: EventExecution) => item.status === "EXECUTION_STATUS_FAILED";

export const canCancel = (item: EventExecution) =>
  item.status === "EXECUTION_STATUS_PENDING" || item.status === "EXECUTION_STATUS_RUNNING";

export const canAcknowledge = (item: EventExecution) => item.status === "EXECUTION_STATUS_FAILED" && !item.acknowledged;

export interface SignatureExample {
  language: string;
  code: string;
}

/** 接收方验证签名的示例：签名内容是 时间戳 + "." + 原始请求体，用验证密钥做 HMAC-SHA256，结果是十六进制。 */
export function signatureExamples(): SignatureExample[] {
  return [
    {
      language: "Node.js",
      code: `const crypto = require("crypto");

// body 必须是收到的原始请求体字符串，不要先解析再重新序列化
function verify(secret, headers, body) {
  const timestamp = headers["x-moox-timestamp"];
  if (Math.abs(Date.now() - Number(timestamp)) > 5 * 60 * 1000) return false;
  const expected = "sha256=" + crypto.createHmac("sha256", secret).update(timestamp + "." + body).digest("hex");
  const actual = headers["x-moox-signature"] || "";
  return expected.length === actual.length && crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(actual));
}`
    },
    {
      language: "Python",
      code: `import hashlib, hmac, time

def verify(secret: str, headers: dict, body: bytes) -> bool:
    timestamp = headers["X-MooX-Timestamp"]
    if abs(time.time() * 1000 - int(timestamp)) > 5 * 60 * 1000:
        return False
    signed = timestamp.encode() + b"." + body
    expected = "sha256=" + hmac.new(secret.encode(), signed, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, headers.get("X-MooX-Signature", ""))`
    },
    {
      language: "Go",
      code: `func verify(secret string, header http.Header, body []byte) bool {
	timestamp := header.Get("X-MooX-Timestamp")
	millis, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || math.Abs(float64(time.Now().UnixMilli()-millis)) > 5*60*1000 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(header.Get("X-MooX-Signature")))
}`
    }
  ];
}

export const RECEIVER_NOTES = [
  "返回 2xx 算成功，其余都算失败。返回 4xx（408、429 除外）不会再试，其余失败会在 24 小时内按 1 分钟、5 分钟、30 分钟、2 小时、6 小时、12 小时重试。",
  "同一件事可能收到不止一次，请用请求头 X-MooX-Event-Id 去重；不保证先后顺序。"
];

export interface HolderSummary {
  /** 生产组合：会被真实平仓，需要二次确认。 */
  live: InstrumentHolder[];
  paper: InstrumentHolder[];
  dust: InstrumentHolder[];
}

/** 预览结果分组：零头不会被平仓，单独列出；其余按是否模拟盘分开。 */
export function summarizeHolders(holders: InstrumentHolder[]): HolderSummary {
  const summary: HolderSummary = { live: [], paper: [], dust: [] };
  for (const holder of holders) {
    if (holder.dust) summary.dust.push(holder);
    else if (holder.paper) summary.paper.push(holder);
    else summary.live.push(holder);
  }
  return summary;
}

export function holderLine(holder: InstrumentHolder): string {
  return `${holder.space_id} / ${holder.logical_account_id}（执行账户 ${holder.trading_account_id}）数量 ${holder.quantity}`;
}

/** 手工登记时可能出现的取值。 */
export const DELIST_EXCHANGES = [
  { value: "binance", label: "币安" },
  { value: "okx", label: "OKX" }
] as const;

export const DELIST_MARKETS = [
  { value: "SPOT", label: "现货" },
  { value: "SWAP", label: "永续合约" }
] as const;

/** 手工登记允许补录已经过去的时间，所以只检查有没有填。 */
export function delistTimeError(delistAtMs: number | null): string {
  if (!delistAtMs || !Number.isFinite(delistAtMs)) return "请选择下架时间";
  return "";
}

/** 事件内容缩进展示；不是合法 JSON 时原样显示。 */
export function prettyPayload(raw: string): string {
  if (!raw.trim()) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

/** 过去的下架时间：服务端会直接跳到核对，不再下单。 */
export function isPastDelistTime(delistAtMs: number, nowMs: number): boolean {
  return delistAtMs <= nowMs;
}
