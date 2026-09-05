import { callControl } from "@/api/admin/http";
import type {
  InstrumentTarget,
  PageRequest,
  PageResponse,
  Strategy,
  StrategyInstance,
  StrategyResult,
  StrategyRunner,
  StrategyRunnerStatus
} from "./strategy-types";

function normalizeStrategy(value: any): Strategy {
  const dsl = value?.dsl_yaml ?? value?.manifest_yaml ?? "";
  return {
    strategy_id: value?.strategy_id ?? "",
    name: value?.strategy_name ?? value?.name ?? "",
    strategy_name: value?.strategy_name ?? value?.name ?? "",
    kind: value?.kind ?? "coin_selection",
    dsl_yaml: dsl,
    manifest_yaml: dsl,
    compiled_json: value?.compiled_json ?? "",
    source_hash: value?.source_hash ?? "",
    created_at: value?.created_at ?? "",
    updated_at: value?.updated_at ?? ""
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

function normalizeRunner(value: any): StrategyRunner {
  return {
    runner_id: value?.runner_id ?? "",
    strategy_id: value?.strategy_id ?? "",
    space_id: value?.space_id ?? "",
    source_view_id: value?.source_view_id ?? "",
    frequency: value?.frequency ?? "",
    logical_account_id: value?.logical_account_id ?? "",
    status: value?.status ?? "DISABLED",
    current_targets: (value?.current_targets ?? []).map(normalizeTarget),
    command_sequence: String(value?.command_sequence ?? "0"),
    last_result_id: value?.last_result_id ?? "",
    last_success_at: value?.last_success_at ?? "",
    last_error: value?.last_error ?? "",
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
    runner_id: value?.runner_id ?? "",
    strategy_id: value?.strategy_id ?? "",
    bar_end_time: value?.bar_end_time ?? value?.period_time ?? value?.trigger_bar_time ?? "",
    period_time: value?.period_time ?? value?.bar_end_time ?? value?.trigger_bar_time ?? "",
    valid_until: value?.valid_until ?? "",
    targets: (value?.targets ?? []).map(normalizeTarget),
    input_hash: value?.input_hash ?? "",
    action: value?.action ?? "",
    debug_info_json: value?.debug_info_json ?? "",
    rule_states_json: value?.rule_states_json ?? "",
    publish_status: value?.publish_status ?? "",
    command_sequence: value?.command_sequence == null ? undefined : String(value.command_sequence),
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

export function createStrategy(strategy: Strategy) {
  const dsl = strategy.dsl_yaml || strategy.manifest_yaml;
  return callControl<{ strategy: Partial<Strategy> }, { strategy: Strategy }>("strategy", "CreateStrategy", {
    strategy: { strategy_id: strategy.strategy_id, dsl_yaml: dsl }
  }).then((rsp) => ({ ...rsp, strategy: normalizeStrategy(rsp.strategy) }));
}

export function updateStrategy(strategy_id: string, dsl_yaml: string) {
  return callControl<{ strategy_id: string; dsl_yaml: string }, { strategy: Strategy }>("strategy", "UpdateStrategy", {
    strategy_id,
    dsl_yaml
  }).then((rsp) => ({ ...rsp, strategy: normalizeStrategy(rsp.strategy) }));
}

export function getStrategy(strategy_id: string) {
  return callControl<{ strategy_id: string }, { strategy: Strategy }>("strategy", "GetStrategy", { strategy_id }).then((rsp) => ({ ...rsp, strategy: normalizeStrategy(rsp.strategy) }));
}

export async function listStrategies(params: PageRequest = {}): Promise<PageResult<Strategy>> {
  const rsp = await callControl<
    { page: Required<PageRequest> },
    { strategies?: Strategy[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategies", { page: pageRequest(params) });
  return {
    items: (rsp.strategies ?? []).map(normalizeStrategy),
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function createInstance(instance: Omit<StrategyInstance, "created_at" | "updated_at" | "session_id"> & { session_id?: string }) {
  return callControl<{ instance: Partial<StrategyInstance> }, { instance: StrategyInstance }>("strategy", "CreateStrategyInstance", {
    instance: {
      instance_id: instance.instance_id,
      strategy_id: instance.strategy_id,
      space_id: instance.space_id,
      input_bindings_json: instance.input_bindings_json,
      logical_account_id: instance.logical_account_id,
      enabled: false
    }
  }).then((rsp) => ({ ...rsp, instance: normalizeInstance(rsp.instance) }));
}

export function getInstance(instance_id: string) {
  return callControl<{ instance_id: string }, { instance: StrategyInstance }>("strategy", "GetStrategyInstance", { instance_id }).then((rsp) => ({
    ...rsp,
    instance: normalizeInstance(rsp.instance)
  }));
}

export async function listInstances(params: PageRequest & { strategy_id?: string; space_id?: string; enabled?: boolean } = {}): Promise<PageResult<StrategyInstance>> {
  const rsp = await callControl<
    { page: Required<PageRequest>; strategy_id?: string; space_id?: string; enabled?: boolean },
    { instances?: StrategyInstance[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyInstances", {
    page: pageRequest(params),
    strategy_id: params.strategy_id || undefined,
    space_id: params.space_id || undefined,
    enabled: params.enabled
  });
  return {
    items: (rsp.instances ?? []).map(normalizeInstance),
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function setInstanceEnabled(instance_id: string, enabled: boolean) {
  return callControl<{ instance_id: string; enabled: boolean }, { instance: StrategyInstance }>("strategy", "SetStrategyInstanceEnabled", {
    instance_id,
    enabled
  }).then((rsp) => ({ ...rsp, instance: normalizeInstance(rsp.instance) }));
}

export function createRunner(runner: StrategyRunner) {
  return callControl<{ runner: Record<string, unknown> }, { runner: StrategyRunner }>("strategy", "CreateRunner", {
    runner: {
      runner_id: runner.runner_id,
      strategy_id: runner.strategy_id,
      space_id: runner.space_id,
      source_view_id: runner.source_view_id,
      frequency: runner.frequency,
      logical_account_id: runner.logical_account_id
    }
  }).then((rsp) => ({ ...rsp, runner: normalizeRunner(rsp.runner) }));
}

export function getRunner(runner_id: string) {
  return callControl<{ runner_id: string }, { runner: StrategyRunner }>("strategy", "GetRunner", { runner_id }).then((rsp) => ({ ...rsp, runner: normalizeRunner(rsp.runner) }));
}

export async function listRunners(
  params: PageRequest & { strategy_id?: string; space_id?: string; status?: StrategyRunnerStatus } = {}
): Promise<PageResult<StrategyRunner>> {
  const rsp = await callControl<
    {
      page: Required<PageRequest>;
      strategy_id?: string;
      space_id?: string;
      status?: string;
    },
    { runners?: StrategyRunner[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListRunners", {
    page: pageRequest(params),
    strategy_id: params.strategy_id || undefined,
    space_id: params.space_id || undefined,
    status: params.status || undefined
  });
  return {
    items: (rsp.runners ?? []).map(normalizeRunner),
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function updateRunner(runner: StrategyRunner) {
  return callControl<{ runner: Record<string, unknown> }, { runner: StrategyRunner }>("strategy", "UpdateRunner", { runner: { runner_id: runner.runner_id, strategy_id: runner.strategy_id, space_id: runner.space_id, source_view_id: runner.source_view_id, frequency: runner.frequency, logical_account_id: runner.logical_account_id } }).then((rsp) => ({ ...rsp, runner: normalizeRunner(rsp.runner) }));
}

export function setRunnerStatus(runner_id: string, status: StrategyRunnerStatus) {
  return callControl<{ runner_id: string; status: StrategyRunnerStatus }, { runner: StrategyRunner }>(
    "strategy",
    "SetRunnerStatus",
    { runner_id, status }
  );
}

export async function listStrategyResults(runner_id: string, params: PageRequest & { instance_id?: string; session_id?: string } = {}): Promise<PageResult<StrategyResult>> {
  const rsp = await callControl<
    { runner_id?: string; instance_id?: string; session_id?: string; page: Required<PageRequest> },
    { results?: StrategyResult[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyResults", {
    ...(runner_id ? { runner_id } : {}),
    ...(params.instance_id ? { instance_id: params.instance_id } : {}),
    ...(params.session_id ? { session_id: params.session_id } : {}),
    page: pageRequest(params)
  });
  return {
    items: (rsp.results ?? []).map(normalizeResult),
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function getStrategyResult(result_id: string) {
  return callControl<{ result_id: string }, { result: StrategyResult }>("strategy", "GetStrategyResult", { result_id }).then((rsp) => ({ ...rsp, result: normalizeResult(rsp.result) }));
}

export async function listStrategyTargets(runner_id: string, instance_id?: string): Promise<{ targets: InstrumentTarget[]; command_sequence: string; session_id?: string; bar_end_time?: string; valid_until?: string }> {
  const rsp = await callControl<{ runner_id?: string; instance_id?: string }, { targets?: InstrumentTarget[]; command_sequence?: string; session_id?: string; bar_end_time?: string; valid_until?: string }>(
    "strategy",
    "ListStrategyTargets",
    { ...(runner_id ? { runner_id } : {}), ...(instance_id ? { instance_id } : {}) }
  );
  return { targets: (rsp.targets ?? []).map(normalizeTarget), command_sequence: String(rsp.command_sequence ?? "0"), session_id: rsp.session_id, bar_end_time: rsp.bar_end_time, valid_until: rsp.valid_until };
}
