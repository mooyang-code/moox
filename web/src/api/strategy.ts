import { callControl } from "@/api/admin/http";
import type {
  EngineStatus,
  InstrumentTarget,
  PageRequest,
  PageResponse,
  Strategy,
  StrategyResult,
  StrategyRunner
} from "./strategy-types";

export interface PageResult<T> {
  items: T[];
  page: PageResponse;
}

function pageRequest(params: PageRequest): Required<PageRequest> {
  return { page: params.page ?? 1, page_size: params.page_size ?? 20 };
}

export function createStrategy(strategy: Strategy) {
  return callControl<{ strategy: Strategy }, { strategy: Strategy }>("strategy", "CreateStrategy", { strategy });
}

export function getStrategy(strategy_id: string) {
  return callControl<{ strategy_id: string }, { strategy: Strategy }>("strategy", "GetStrategy", { strategy_id });
}

export async function listStrategies(params: PageRequest = {}): Promise<PageResult<Strategy>> {
  const rsp = await callControl<
    { page: Required<PageRequest> },
    { strategies?: Strategy[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategies", { page: pageRequest(params) });
  return {
    items: rsp.strategies ?? [],
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function createRunner(runner: StrategyRunner) {
  return callControl<{ runner: StrategyRunner }, { runner: StrategyRunner }>("strategy", "CreateRunner", { runner });
}

export function getRunner(runner_id: string) {
  return callControl<{ runner_id: string }, { runner: StrategyRunner }>("strategy", "GetRunner", { runner_id });
}

export async function listRunners(
  params: PageRequest & { strategy_id?: string; space_id?: string; status?: string } = {}
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
    items: rsp.runners ?? [],
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function updateRunner(runner: StrategyRunner) {
  return callControl<{ runner: StrategyRunner }, { runner: StrategyRunner }>("strategy", "UpdateRunner", { runner });
}

export function setRunnerStatus(runner_id: string, status: string) {
  return callControl<{ runner_id: string; status: string }, { runner: StrategyRunner }>("strategy", "SetRunnerStatus", {
    runner_id,
    status
  });
}

export function runOnce(req: { runner_id: string; trigger_bar_time: string; namespace: string; data_json: string }) {
  return callControl<typeof req, { result?: StrategyResult; accepted: boolean }>("strategy", "RunOnce", req);
}

export async function listStrategyResults(runner_id: string, params: PageRequest = {}): Promise<PageResult<StrategyResult>> {
  const rsp = await callControl<
    { runner_id: string; page: Required<PageRequest> },
    { results?: StrategyResult[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyResults", { runner_id, page: pageRequest(params) });
  return {
    items: rsp.results ?? [],
    page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size }
  };
}

export function getStrategyResult(result_id: string) {
  return callControl<{ result_id: string }, { result: StrategyResult }>("strategy", "GetStrategyResult", { result_id });
}

export async function listStrategyTargets(runner_id: string): Promise<{ targets: InstrumentTarget[]; command_sequence: string }> {
  const rsp = await callControl<{ runner_id: string }, { targets?: InstrumentTarget[]; command_sequence?: string }>(
    "strategy",
    "ListStrategyTargets",
    { runner_id }
  );
  return { targets: rsp.targets ?? [], command_sequence: rsp.command_sequence ?? "0" };
}

export async function getEngineStatus(): Promise<EngineStatus> {
  const rsp = await callControl<Record<string, never>, Partial<EngineStatus>>("strategy", "GetEngineStatus", {});
  return { workers: rsp.workers ?? 0, ready_workers: rsp.ready_workers ?? 0 };
}
