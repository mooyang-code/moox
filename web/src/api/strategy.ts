import { callControl } from "@/api/admin/http";
import type {
  InstrumentTarget,
  PageRequest,
  PageResponse,
  Replay,
  ReplayBar,
  StartedReplay,
  StartReplayRequest,
  Strategy,
  StrategyInstance,
  StrategyResult,
  StrategyResultDetail,
  StrategyResultItem,
  StrategyTargetSnapshot,
  ValidateStrategyResult
} from "./strategy-types";

function normalizeStrategy(value: any): Strategy {
  return {
    strategy_id: value?.strategy_id ?? "",
    name: value?.name ?? "",
    dsl_yaml: value?.dsl_yaml ?? "",
    dsl_hash: value?.dsl_hash ?? "",
    created_at: value?.created_at ?? "",
    updated_at: value?.updated_at ?? ""
  };
}

function normalizeInstance(value: any): StrategyInstance {
  return {
    instance_id: value?.instance_id ?? "",
    strategy_id: value?.strategy_id ?? "",
    space_id: value?.space_id ?? "",
    view_id: value?.view_id ?? "",
    logical_account_id: value?.logical_account_id ?? "",
    enabled: Boolean(value?.enabled),
    session_id: value?.session_id ?? "",
    resolved_json: value?.resolved_json || "{}",
    health: value?.health || "ok",
    created_at: value?.created_at ?? "",
    updated_at: value?.updated_at ?? ""
  };
}

function normalizeTarget(value: any): InstrumentTarget {
  return { instrument_id: value?.instrument_id ?? "", target_weight: value?.target_weight ?? "0" };
}

function normalizeResult(value: any): StrategyResult {
  return {
    result_id: value?.result_id ?? "",
    instance_id: value?.instance_id ?? "",
    session_id: value?.session_id ?? "",
    bar_end_time: value?.bar_end_time ?? "",
    valid_until: value?.valid_until ?? "",
    status: value?.status ?? "",
    skip_reason: value?.skip_reason ?? "",
    dsl_hash: value?.dsl_hash ?? "",
    targets: (value?.targets ?? []).map(normalizeTarget),
    summary_json: value?.summary_json || "{}",
    input_json: value?.input_json || "{}",
    rule_states_json: value?.rule_states_json || "{}",
    publish_status: value?.publish_status ?? "",
    created_at: value?.created_at ?? ""
  };
}

function normalizeItem(value: any): StrategyResultItem {
  return {
    rule_id: value?.rule_id ?? "",
    instrument_id: value?.instrument_id ?? "",
    stage: value?.stage ?? "",
    score: value?.score ?? "",
    rank: Number(value?.rank ?? 0),
    weight: value?.weight ?? "",
    reason: value?.reason ?? ""
  };
}

function normalizeReplay(value: any): Replay {
  return {
    replay_id: value?.replay_id ?? "",
    strategy_id: value?.strategy_id ?? "",
    dsl_yaml: value?.dsl_yaml ?? "",
    space_id: value?.space_id ?? "",
    view_id: value?.view_id ?? "",
    start_time: value?.start_time ?? "",
    end_time: value?.end_time ?? "",
    fee_bps: Number(value?.fee_bps ?? 0),
    status: value?.status ?? "",
    progress_time: value?.progress_time ?? "",
    metrics_json: value?.metrics_json || "{}",
    error: value?.error ?? "",
    created_at: value?.created_at ?? "",
    updated_at: value?.updated_at ?? "",
    dsl_hash: value?.dsl_hash ?? "",
    instance_id: value?.instance_id ?? "",
    session_id: value?.session_id ?? ""
  };
}

function normalizeReplayBar(value: any): ReplayBar {
  return {
    bar_end_time: value?.bar_end_time ?? "",
    status: value?.status ?? "",
    targets: (value?.targets ?? []).map(normalizeTarget),
    positions_json: value?.positions_json || "{}",
    summary_json: value?.summary_json || "{}",
    bar_return: Number(value?.bar_return ?? 0),
    equity: Number(value?.equity ?? 0),
    turnover: Number(value?.turnover ?? 0),
    fee: Number(value?.fee ?? 0),
    holdings: Number(value?.holdings ?? 0),
    frozen: Number(value?.frozen ?? 0),
    skip_reason: value?.skip_reason ?? "",
    unfilled: Number(value?.unfilled ?? 0),
    liquidated: Number(value?.liquidated ?? 0)
  };
}

export interface PageResult<T> {
  items: T[];
  page: PageResponse;
}

function pageRequest(params: PageRequest): Required<PageRequest> {
  return { page: params.page ?? 1, page_size: params.page_size ?? 20 };
}

function ensureResponse<T extends Record<string, any>>(response: T): T {
  const code = response?.ret_info?.code;
  if (code !== undefined && code !== 0 && code !== "0" && code !== "SUCCESS") {
    throw new Error(response.ret_info?.msg || `策略服务返回错误：${String(code)}`);
  }
  return response;
}

/** 请求选项：silent 为 true 时失败不弹全局错误提示（后台轮询由页面自己提示）。 */
export interface RequestOptions {
  silent?: boolean;
}

async function call<Req extends object, Rsp extends Record<string, any>>(
  method: string,
  request: Req,
  options: RequestOptions = {}
): Promise<Rsp> {
  return ensureResponse(
    await callControl<Req, Rsp>("strategy", method, request, options.silent ? { silentError: true } : undefined)
  );
}

export async function createStrategy(strategy: { strategy_id?: string; dsl_yaml: string }) {
  const response = await call<{ strategy: typeof strategy }, { strategy?: Strategy }>("CreateStrategy", { strategy });
  return normalizeStrategy(response.strategy);
}

export async function updateStrategy(strategy_id: string, dsl_yaml: string) {
  const response = await call<{ strategy_id: string; dsl_yaml: string }, { strategy?: Strategy }>("UpdateStrategy", {
    strategy_id,
    dsl_yaml
  });
  return normalizeStrategy(response.strategy);
}

export async function getStrategy(strategy_id: string) {
  const response = await call<{ strategy_id: string }, { strategy?: Strategy }>("GetStrategy", { strategy_id });
  return normalizeStrategy(response.strategy);
}

export async function deleteStrategy(strategy_id: string) {
  await call<{ strategy_id: string }, Record<string, never>>("DeleteStrategy", { strategy_id });
}

export async function listStrategies(params: PageRequest = {}): Promise<PageResult<Strategy>> {
  const response = await call<
    { page: Required<PageRequest> },
    { strategies?: Strategy[]; total?: number; page?: number; page_size?: number }
  >("ListStrategies", { page: pageRequest(params) });
  return {
    items: (response.strategies ?? []).map(normalizeStrategy),
    page: { total: Number(response.total ?? 0), page: response.page, page_size: response.page_size }
  };
}

/** 校验 DSL；给出 view_id 时解析绑定并在最新周期试算一次（不落库）。 */
export async function validateStrategy(dsl_yaml: string, view_id = ""): Promise<ValidateStrategyResult> {
  const response = await callControl<{ dsl_yaml: string; view_id?: string }, any>("strategy", "ValidateStrategy", {
    dsl_yaml,
    view_id: view_id || undefined
  });
  const code = response?.ret_info?.code;
  const diagnostics: string[] = response?.diagnostics ?? [];
  if (code !== undefined && code !== 0 && code !== "0" && code !== "SUCCESS") {
    const message = response?.ret_info?.msg || `策略服务返回错误：${String(code)}`;
    return { diagnostics: diagnostics.length ? diagnostics : [message], resolved_json: "", trial: null, trial_items: [] };
  }
  return {
    diagnostics,
    resolved_json: response?.resolved_json ?? "",
    trial: response?.trial ? normalizeResult(response.trial) : null,
    trial_items: (response?.trial_items ?? []).map(normalizeItem)
  };
}

export async function createInstance(instance: {
  instance_id?: string;
  strategy_id: string;
  view_id: string;
  logical_account_id?: string;
}) {
  const response = await call<{ instance: typeof instance & { enabled: false } }, { instance?: StrategyInstance }>(
    "CreateStrategyInstance",
    { instance: { ...instance, enabled: false } }
  );
  return normalizeInstance(response.instance);
}

export async function updateInstance(instance: {
  instance_id: string;
  strategy_id?: string;
  view_id?: string;
  logical_account_id?: string;
}) {
  const response = await call<{ instance: typeof instance }, { instance?: StrategyInstance }>("UpdateStrategyInstance", {
    instance
  });
  return normalizeInstance(response.instance);
}

export async function getInstance(instance_id: string) {
  const response = await call<{ instance_id: string }, { instance?: StrategyInstance }>("GetStrategyInstance", { instance_id });
  return normalizeInstance(response.instance);
}

export async function deleteInstance(instance_id: string) {
  await call<{ instance_id: string }, Record<string, never>>("DeleteStrategyInstance", { instance_id });
}

export async function listInstances(
  params: PageRequest & { strategy_id?: string; enabled?: boolean } = {}
): Promise<PageResult<StrategyInstance>> {
  const response = await call<
    { page: Required<PageRequest>; strategy_id?: string; enabled?: boolean },
    { instances?: StrategyInstance[]; total?: number; page?: number; page_size?: number }
  >("ListStrategyInstances", {
    page: pageRequest(params),
    strategy_id: params.strategy_id || undefined,
    enabled: params.enabled
  });
  return {
    items: (response.instances ?? []).map(normalizeInstance),
    page: { total: Number(response.total ?? 0), page: response.page, page_size: response.page_size }
  };
}

export async function setInstanceEnabled(instance_id: string, enabled: boolean) {
  const response = await call<{ instance_id: string; enabled: boolean }, { instance?: StrategyInstance }>(
    "SetStrategyInstanceEnabled",
    { instance_id, enabled }
  );
  return normalizeInstance(response.instance);
}

export async function listStrategyResults(
  instance_id: string,
  params: PageRequest & { session_id?: string } = {}
): Promise<PageResult<StrategyResult>> {
  const response = await call<
    { instance_id: string; session_id?: string; page: Required<PageRequest> },
    { results?: StrategyResult[]; total?: number; page?: number; page_size?: number }
  >("ListStrategyResults", { instance_id, session_id: params.session_id || undefined, page: pageRequest(params) });
  return {
    items: (response.results ?? []).map(normalizeResult),
    page: { total: Number(response.total ?? 0), page: response.page, page_size: response.page_size }
  };
}

/** 读取一个结果及其解释明细、所属会话固化的 DSL 版本与绑定解析。 */
export async function getStrategyResult(result_id: string): Promise<StrategyResultDetail> {
  const response = await call<
    { result_id: string },
    { result?: StrategyResult; items?: StrategyResultItem[]; dsl_yaml?: string; resolved_json?: string }
  >("GetStrategyResult", { result_id });
  return {
    result: normalizeResult(response.result),
    items: (response.items ?? []).map(normalizeItem),
    dsl_yaml: response.dsl_yaml ?? "",
    resolved_json: response.resolved_json || "{}"
  };
}

export async function listStrategyTargets(instance_id: string): Promise<StrategyTargetSnapshot> {
  const response = await call<
    { instance_id: string },
    { targets?: InstrumentTarget[]; session_id?: string; bar_end_time?: string; valid_until?: string; result_id?: string }
  >("ListStrategyTargets", { instance_id });
  return {
    targets: (response.targets ?? []).map(normalizeTarget),
    session_id: response.session_id ?? "",
    bar_end_time: response.bar_end_time ?? "",
    valid_until: response.valid_until ?? "",
    result_id: response.result_id ?? ""
  };
}

export async function startReplay(request: StartReplayRequest): Promise<StartedReplay> {
  const response = await call<
    StartReplayRequest,
    { replay?: Replay; bar_count?: number; first_bar_end?: string; last_bar_end?: string }
  >("StartReplay", request);
  return {
    replay: normalizeReplay(response.replay),
    bar_count: Number(response.bar_count ?? 0),
    first_bar_end: response.first_bar_end ?? "",
    last_bar_end: response.last_bar_end ?? ""
  };
}

export async function getReplay(replay_id: string, options: RequestOptions = {}) {
  const response = await call<{ replay_id: string }, { replay?: Replay }>("GetReplay", { replay_id }, options);
  return normalizeReplay(response.replay);
}

export async function listReplays(params: PageRequest = {}, options: RequestOptions = {}): Promise<PageResult<Replay>> {
  const response = await call<
    { page: Required<PageRequest> },
    { replays?: Replay[]; total?: number; page?: number; page_size?: number }
  >("ListReplays", { page: pageRequest(params) }, options);
  return {
    items: (response.replays ?? []).map(normalizeReplay),
    page: { total: Number(response.total ?? 0), page: response.page, page_size: response.page_size }
  };
}

/**
 * 分页读取回放的周期记录。brief 只返回曲线与摘要字段（每页最多 5000 根），用于权益曲线；
 * 完整记录含目标与持仓（每页最多 100 根），用于表格当前页。
 */
export async function listReplayBars(
  replay_id: string,
  params: PageRequest & { brief?: boolean } = {},
  options: RequestOptions = {}
): Promise<PageResult<ReplayBar>> {
  const response = await call<
    { replay_id: string; page: Required<PageRequest>; brief: boolean },
    { bars?: ReplayBar[]; total?: number; page?: number; page_size?: number }
  >(
    "ListReplayBars",
    {
      replay_id,
      page: { page: params.page ?? 1, page_size: params.page_size ?? 50 },
      brief: Boolean(params.brief)
    },
    options
  );
  return {
    items: (response.bars ?? []).map(normalizeReplayBar),
    page: { total: Number(response.total ?? 0), page: response.page, page_size: response.page_size }
  };
}

export async function cancelReplay(replay_id: string) {
  const response = await call<{ replay_id: string }, { replay?: Replay }>("CancelReplay", { replay_id });
  return normalizeReplay(response.replay);
}
