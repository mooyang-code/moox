import { callControl } from "@/api/admin/http";

// 事件中心（callback-dispatcher）的接口：全局服务，不分空间。字段以
// modules/callbackdispatcher/proto/callbackdispatcher.proto 为准；时间一律是 Unix 毫秒。
// 后端 JSON 以数字输出枚举，这里在 API 边界统一转换为枚举名，页面只比较名称。

export const SERVICE = "callback-dispatcher";

export type ActionType = "ACTION_TYPE_UNSPECIFIED" | "ACTION_TYPE_NOTIFY_URL" | "ACTION_TYPE_SYSTEM";
export type SystemAction = "SYSTEM_ACTION_UNSPECIFIED" | "SYSTEM_ACTION_SELL_DELISTED_HOLDINGS";
export type ExecutionStatus =
  | "EXECUTION_STATUS_UNSPECIFIED"
  | "EXECUTION_STATUS_PENDING"
  | "EXECUTION_STATUS_RUNNING"
  | "EXECUTION_STATUS_SUCCEEDED"
  | "EXECUTION_STATUS_FAILED"
  | "EXECUTION_STATUS_CANCELED";
export type DelistStepKind =
  | "DELIST_STEP_KIND_UNSPECIFIED"
  | "DELIST_STEP_KIND_BLOCK"
  | "DELIST_STEP_KIND_FLATTEN"
  | "DELIST_STEP_KIND_VERIFY";

const ACTION_TYPE_NAMES: ActionType[] = ["ACTION_TYPE_UNSPECIFIED", "ACTION_TYPE_NOTIFY_URL", "ACTION_TYPE_SYSTEM"];
const SYSTEM_ACTION_NAMES: SystemAction[] = ["SYSTEM_ACTION_UNSPECIFIED", "SYSTEM_ACTION_SELL_DELISTED_HOLDINGS"];
const EXECUTION_STATUS_NAMES: ExecutionStatus[] = [
  "EXECUTION_STATUS_UNSPECIFIED",
  "EXECUTION_STATUS_PENDING",
  "EXECUTION_STATUS_RUNNING",
  "EXECUTION_STATUS_SUCCEEDED",
  "EXECUTION_STATUS_FAILED",
  "EXECUTION_STATUS_CANCELED"
];
const DELIST_STEP_KIND_NAMES: DelistStepKind[] = [
  "DELIST_STEP_KIND_UNSPECIFIED",
  "DELIST_STEP_KIND_BLOCK",
  "DELIST_STEP_KIND_FLATTEN",
  "DELIST_STEP_KIND_VERIFY"
];

export function enumName<T extends string>(value: unknown, names: readonly T[]): T {
  if (typeof value === "number") return names[value] ?? names[0];
  if (typeof value === "string" && /^\d+$/.test(value)) return names[Number(value)] ?? names[0];
  if (typeof value === "string" && (names as readonly string[]).includes(value)) return value as T;
  return names[0];
}

/** int64 在 JSON 里可能是数字也可能是字符串；缺省（值为 0 时服务端会省略）按 0 处理。 */
export function toNumber(value: unknown): number {
  const n = Number(value ?? 0);
  return Number.isFinite(n) ? n : 0;
}

export interface PageReq {
  page?: number;
  size?: number;
}

export interface PageResult {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
}

/** 限定范围里的一项：不同 field 之间是“且”，同一 field 的 values 之间是“或”。 */
export interface ScopeFilter {
  field: string;
  values: string[];
}

export interface ScopeField {
  field: string;
  title: string;
  /** 建议的取值；为空表示自由填写。 */
  options: string[];
}

export interface SubscribableEvent {
  event_name: string;
  version: number;
  title: string;
  description: string;
  scope_fields: ScopeField[];
}

export interface RuleLastExecution {
  status: ExecutionStatus;
  at_ms: number;
}

export interface EventRule {
  rule_id: string;
  name: string;
  event_name: string;
  scope: ScopeFilter[];
  action_type: ActionType;
  notify_url: string;
  allow_private_address: boolean;
  /** 验证密钥的末 4 位，用于辨认。 */
  secret_hint: string;
  system_action: SystemAction;
  system: boolean;
  enabled: boolean;
  created_at_ms: number;
  updated_at_ms: number;
  last_execution?: RuleLastExecution;
}

export interface EventExecution {
  execution_id: string;
  rule_id: string;
  rule_name: string;
  action_type: ActionType;
  event_id: string;
  event_name: string;
  space_id: string;
  subject_id: string;
  status: ExecutionStatus;
  attempts: number;
  last_error: string;
  occurred_at_ms: number;
  created_at_ms: number;
  finished_at_ms: number;
  duration_ms: number;
  next_run_at_ms: number;
  acknowledged: boolean;
}

export interface DeliveryAttempt {
  attempt: number;
  at_ms: number;
  http_status: number;
  duration_ms: number;
  response_snippet: string;
  error: string;
}

export interface DelistStep {
  round: number;
  kind: DelistStepKind;
  space_id: string;
  logical_account_id: string;
  exchange: string;
  operator_action_id: string;
  status: string;
  attempts: number;
  result: string;
  error: string;
  updated_at_ms: number;
}

export interface EventExecutionDetail {
  execution: EventExecution;
  payload_json: string;
  attempts: DeliveryAttempt[];
  delist_steps: DelistStep[];
}

export interface CreateEventRuleReq {
  name: string;
  event_name: string;
  scope: ScopeFilter[];
  notify_url: string;
  allow_private_address: boolean;
  enabled?: boolean;
}

/** 只改带了的字段；scope 带上就整体替换（items 为空表示不限）。 */
export interface UpdateEventRuleReq {
  rule_id: string;
  name?: string;
  scope?: { items: ScopeFilter[] };
  notify_url?: string;
  allow_private_address?: boolean;
  enabled?: boolean;
}

export interface TestEventRuleResult {
  http_status: number;
  duration_ms: number;
  response_snippet: string;
  success: boolean;
}

export interface ListEventExecutionsReq {
  rule_id?: string;
  status?: ExecutionStatus;
  page?: PageReq;
}

export function normalizeRule(raw: Partial<EventRule> & Record<string, unknown>): EventRule {
  const last = raw.last_execution as { status?: unknown; at_ms?: unknown } | undefined;
  return {
    rule_id: String(raw.rule_id ?? ""),
    name: String(raw.name ?? ""),
    event_name: String(raw.event_name ?? ""),
    scope: normalizeScope(raw.scope),
    action_type: enumName(raw.action_type, ACTION_TYPE_NAMES),
    notify_url: String(raw.notify_url ?? ""),
    allow_private_address: Boolean(raw.allow_private_address),
    secret_hint: String(raw.secret_hint ?? ""),
    system_action: enumName(raw.system_action, SYSTEM_ACTION_NAMES),
    system: Boolean(raw.system),
    enabled: Boolean(raw.enabled),
    created_at_ms: toNumber(raw.created_at_ms),
    updated_at_ms: toNumber(raw.updated_at_ms),
    last_execution: last ? { status: enumName(last.status, EXECUTION_STATUS_NAMES), at_ms: toNumber(last.at_ms) } : undefined
  };
}

function normalizeScope(raw: unknown): ScopeFilter[] {
  if (!Array.isArray(raw)) return [];
  return raw.map(item => ({
    field: String((item as ScopeFilter).field ?? ""),
    values: ((item as ScopeFilter).values ?? []).map(String)
  }));
}

export function normalizeExecution(raw: Partial<EventExecution> & Record<string, unknown>): EventExecution {
  return {
    execution_id: String(raw.execution_id ?? ""),
    rule_id: String(raw.rule_id ?? ""),
    rule_name: String(raw.rule_name ?? ""),
    action_type: enumName(raw.action_type, ACTION_TYPE_NAMES),
    event_id: String(raw.event_id ?? ""),
    event_name: String(raw.event_name ?? ""),
    space_id: String(raw.space_id ?? ""),
    subject_id: String(raw.subject_id ?? ""),
    status: enumName(raw.status, EXECUTION_STATUS_NAMES),
    attempts: toNumber(raw.attempts),
    last_error: String(raw.last_error ?? ""),
    occurred_at_ms: toNumber(raw.occurred_at_ms),
    created_at_ms: toNumber(raw.created_at_ms),
    finished_at_ms: toNumber(raw.finished_at_ms),
    duration_ms: toNumber(raw.duration_ms),
    next_run_at_ms: toNumber(raw.next_run_at_ms),
    acknowledged: Boolean(raw.acknowledged)
  };
}

function normalizeEvent(raw: Partial<SubscribableEvent> & Record<string, unknown>): SubscribableEvent {
  return {
    event_name: String(raw.event_name ?? ""),
    version: toNumber(raw.version),
    title: String(raw.title ?? ""),
    description: String(raw.description ?? ""),
    scope_fields: (raw.scope_fields ?? []).map(field => ({
      field: String(field.field ?? ""),
      title: String(field.title ?? ""),
      options: (field.options ?? []).map(String)
    }))
  };
}

export interface SubscribableCatalog {
  events: SubscribableEvent[];
  /** 已经上线的系统动作；不在里面的只记录执行记录，不会真的禁止买入或平仓。 */
  availableSystemActions: SystemAction[];
}

export async function listSubscribableEvents(): Promise<SubscribableCatalog> {
  const rsp = await callControl<
    Record<string, never>,
    { events?: SubscribableEvent[]; available_system_actions?: (number | string)[] }
  >(SERVICE, "ListSubscribableEvents", {});
  return {
    events: (rsp.events ?? []).map(event => normalizeEvent(event as never)),
    availableSystemActions: (rsp.available_system_actions ?? []).map(item => enumName(item, SYSTEM_ACTION_NAMES))
  };
}

export async function listEventRules(page: PageReq = {}): Promise<{ rules: EventRule[]; page_result?: PageResult }> {
  const rsp = await callControl<{ page: PageReq }, { rules?: EventRule[]; page_result?: PageResult }>(SERVICE, "ListEventRules", {
    page
  });
  return { rules: (rsp.rules ?? []).map(rule => normalizeRule(rule as never)), page_result: rsp.page_result };
}

export async function createEventRule(req: CreateEventRuleReq): Promise<{ rule: EventRule; signing_secret: string }> {
  const rsp = await callControl<CreateEventRuleReq, { rule: EventRule; signing_secret?: string }>(
    SERVICE,
    "CreateEventRule",
    req
  );
  return { rule: normalizeRule(rsp.rule as never), signing_secret: rsp.signing_secret ?? "" };
}

export async function updateEventRule(req: UpdateEventRuleReq): Promise<EventRule> {
  const rsp = await callControl<UpdateEventRuleReq, { rule: EventRule }>(SERVICE, "UpdateEventRule", req);
  return normalizeRule(rsp.rule as never);
}

export async function deleteEventRule(ruleId: string): Promise<void> {
  await callControl<{ rule_id: string }, unknown>(SERVICE, "DeleteEventRule", { rule_id: ruleId });
}

export async function testEventRule(ruleId: string): Promise<TestEventRuleResult> {
  const rsp = await callControl<{ rule_id: string }, Partial<TestEventRuleResult>>(SERVICE, "TestEventRule", {
    rule_id: ruleId
  });
  return {
    http_status: toNumber(rsp.http_status),
    duration_ms: toNumber(rsp.duration_ms),
    response_snippet: rsp.response_snippet ?? "",
    success: Boolean(rsp.success)
  };
}

export async function listEventExecutions(
  req: ListEventExecutionsReq
): Promise<{ executions: EventExecution[]; page_result?: PageResult }> {
  const rsp = await callControl<ListEventExecutionsReq, { executions?: EventExecution[]; page_result?: PageResult }>(
    SERVICE,
    "ListEventExecutions",
    req
  );
  return {
    executions: (rsp.executions ?? []).map(item => normalizeExecution(item as never)),
    page_result: rsp.page_result
  };
}

export async function getEventExecution(executionId: string): Promise<EventExecutionDetail> {
  const rsp = await callControl<{ execution_id: string }, { detail?: Record<string, unknown> }>(SERVICE, "GetEventExecution", {
    execution_id: executionId
  });
  const detail = (rsp.detail ?? {}) as Record<string, unknown>;
  return {
    execution: normalizeExecution((detail.execution ?? {}) as never),
    payload_json: String(detail.payload_json ?? ""),
    attempts: ((detail.attempts ?? []) as Partial<DeliveryAttempt>[]).map(item => ({
      attempt: toNumber(item.attempt),
      at_ms: toNumber(item.at_ms),
      http_status: toNumber(item.http_status),
      duration_ms: toNumber(item.duration_ms),
      response_snippet: item.response_snippet ?? "",
      error: item.error ?? ""
    })),
    delist_steps: ((detail.delist_steps ?? []) as Partial<DelistStep>[]).map(item => ({
      round: toNumber(item.round),
      kind: enumName(item.kind, DELIST_STEP_KIND_NAMES),
      space_id: item.space_id ?? "",
      logical_account_id: item.logical_account_id ?? "",
      exchange: item.exchange ?? "",
      operator_action_id: item.operator_action_id ?? "",
      status: item.status ?? "",
      attempts: toNumber(item.attempts),
      result: item.result ?? "",
      error: item.error ?? "",
      updated_at_ms: toNumber(item.updated_at_ms)
    }))
  };
}

async function executionAction(method: string, executionId: string): Promise<EventExecution> {
  const rsp = await callControl<{ execution_id: string }, { execution?: EventExecution }>(SERVICE, method, {
    execution_id: executionId
  });
  return normalizeExecution((rsp.execution ?? {}) as never);
}

export const retryEventExecution = (executionId: string) => executionAction("RetryEventExecution", executionId);
export const cancelEventExecution = (executionId: string) => executionAction("CancelEventExecution", executionId);
export const acknowledgeEventExecution = (executionId: string) => executionAction("AcknowledgeEventExecution", executionId);
