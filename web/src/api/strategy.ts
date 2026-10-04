import { callControl } from "@/api/admin/http";
import type {
  InstrumentTarget,
  PageRequest,
  PageResponse,
  Strategy,
  StrategyInstance,
  StrategyResult,
  StrategyTargetSnapshot
} from "./strategy-types";

function normalizeStrategy(value: any): Strategy {
  return {
    strategy_id: value?.strategy_id ?? "",
    name: value?.strategy_name ?? value?.name ?? "",
    dsl_yaml: value?.dsl_yaml ?? "",
    created_at: value?.created_at ?? ""
  };
}

function normalizeInstance(value: any): StrategyInstance {
  return {
    instance_id: value?.instance_id ?? "",
    strategy_id: value?.strategy_id ?? "",
    space_id: value?.space_id ?? "",
    input_bindings_json: value?.input_bindings_json ?? "{}",
    logical_account_id: value?.logical_account_id ?? "",
    enabled: Boolean(value?.enabled),
    session_id: value?.session_id ?? "",
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
    bar_end_time: value?.period_time ?? value?.bar_end_time ?? "",
    period_time: value?.period_time ?? value?.bar_end_time ?? "",
    valid_until: value?.valid_until ?? "",
    targets: (value?.targets ?? []).map(normalizeTarget),
    rule_states_json: value?.rule_states_json ?? "",
    publish_status: value?.publish_status ?? "",
    created_at: value?.created_at ?? ""
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
    throw new Error(response.ret_info?.msg || `策略服务返回错误: ${String(code)}`);
  }
  return response;
}

export function createStrategy(strategy: Pick<Strategy, "strategy_id" | "dsl_yaml">) {
  return callControl<{ strategy: Pick<Strategy, "strategy_id" | "dsl_yaml"> }, { strategy: Strategy }>("strategy", "CreateStrategy", {
    strategy
  }).then((response) => {
    ensureResponse(response);
    return { ...response, strategy: normalizeStrategy(response.strategy) };
  });
}

export function updateStrategy(strategy_id: string, dsl_yaml: string) {
  return callControl<{ strategy_id: string; dsl_yaml: string }, { strategy: Strategy }>("strategy", "UpdateStrategy", {
    strategy_id,
    dsl_yaml
  }).then((response) => {
    ensureResponse(response);
    return { ...response, strategy: normalizeStrategy(response.strategy) };
  });
}

export function getStrategy(strategy_id: string) {
  return callControl<{ strategy_id: string }, { strategy: Strategy }>("strategy", "GetStrategy", { strategy_id }).then((response) => {
    ensureResponse(response);
    return { ...response, strategy: normalizeStrategy(response.strategy) };
  });
}

export async function listStrategies(params: PageRequest = {}): Promise<PageResult<Strategy>> {
  const response = await callControl<
    { page: Required<PageRequest> },
    { strategies?: Strategy[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategies", { page: pageRequest(params) });
  ensureResponse(response);
  return {
    items: (response.strategies ?? []).map(normalizeStrategy),
    page: { total: response.total ?? 0, page: response.page, page_size: response.page_size }
  };
}

export function createInstance(instance: Pick<StrategyInstance, "instance_id" | "strategy_id" | "space_id" | "input_bindings_json" | "logical_account_id">) {
  return callControl<{ instance: Pick<StrategyInstance, "instance_id" | "strategy_id" | "space_id" | "input_bindings_json" | "logical_account_id"> & { enabled: false } }, { instance: StrategyInstance }>(
    "strategy",
    "CreateStrategyInstance",
    { instance: { ...instance, enabled: false } }
  ).then((response) => {
    ensureResponse(response);
    return { ...response, instance: normalizeInstance(response.instance) };
  });
}

export function getInstance(instance_id: string) {
  return callControl<{ instance_id: string }, { instance: StrategyInstance }>("strategy", "GetStrategyInstance", { instance_id }).then((response) => {
    ensureResponse(response);
    return { ...response, instance: normalizeInstance(response.instance) };
  });
}

export async function listInstances(params: PageRequest & { strategy_id?: string; space_id?: string; enabled?: boolean } = {}): Promise<PageResult<StrategyInstance>> {
  const response = await callControl<
    { page: Required<PageRequest>; strategy_id?: string; space_id?: string; enabled?: boolean },
    { instances?: StrategyInstance[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyInstances", {
    page: pageRequest(params),
    strategy_id: params.strategy_id || undefined,
    space_id: params.space_id || undefined,
    enabled: params.enabled
  });
  ensureResponse(response);
  return {
    items: (response.instances ?? []).map(normalizeInstance),
    page: { total: response.total ?? 0, page: response.page, page_size: response.page_size }
  };
}

export function setInstanceEnabled(instance_id: string, enabled: boolean) {
  return callControl<{ instance_id: string; enabled: boolean }, { instance: StrategyInstance }>("strategy", "SetStrategyInstanceEnabled", {
    instance_id,
    enabled
  }).then((response) => {
    ensureResponse(response);
    return { ...response, instance: normalizeInstance(response.instance) };
  });
}

export async function listStrategyResults(instance_id: string, params: PageRequest & { session_id?: string } = {}): Promise<PageResult<StrategyResult>> {
  const response = await callControl<
    { instance_id: string; session_id?: string; page: Required<PageRequest> },
    { results?: StrategyResult[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyResults", {
    instance_id,
    session_id: params.session_id || undefined,
    page: pageRequest(params)
  });
  ensureResponse(response);
  return {
    items: (response.results ?? []).map(normalizeResult),
    page: { total: response.total ?? 0, page: response.page, page_size: response.page_size }
  };
}

export function getStrategyResult(result_id: string) {
  return callControl<{ result_id: string }, { result: StrategyResult }>("strategy", "GetStrategyResult", { result_id }).then((response) => {
    ensureResponse(response);
    return { ...response, result: normalizeResult(response.result) };
  });
}

export function listStrategyTargets(instance_id: string): Promise<StrategyTargetSnapshot> {
  return callControl<{ instance_id: string }, { targets?: InstrumentTarget[]; session_id?: string; bar_end_time?: string; valid_until?: string }>(
    "strategy",
    "ListStrategyTargets",
    { instance_id }
  ).then((response) => {
    ensureResponse(response);
    return {
      targets: (response.targets ?? []).map(normalizeTarget),
      session_id: response.session_id ?? "",
      bar_end_time: response.bar_end_time ?? "",
      valid_until: response.valid_until ?? ""
    };
  });
}
