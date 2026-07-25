import { callControl } from "@/api/admin/http";
import type {
  PageRequest,
  PageResponse,
  PerformanceSource,
  RunningStrategySummary,
  StrategyHealth,
  StrategyOverview,
  StrategyPerformance,
  StrategyRun,
  TargetWeight
} from "./strategy-types";

export interface RunningStrategyFilter extends PageRequest {
  space_id?: string;
  status?: string;
  mode?: string;
  strategy_id?: string;
}

export interface PageResult<T> {
  items: T[];
  page: PageResponse;
}

export interface StrategyCapabilities {
  live_execution_enabled: boolean;
}

export async function getStrategyCapabilities(): Promise<StrategyCapabilities> {
  const rsp = await callControl<Record<string, never>, { live_execution_enabled?: boolean }>("strategy", "GetEngineStatus", {});
  return { live_execution_enabled: rsp.live_execution_enabled === true };
}

export function normalizePerformance(input: { groups?: StrategyPerformance[] } | StrategyPerformance): {
  groups: StrategyPerformance[];
} {
  return "groups" in input && Array.isArray(input.groups) ? { groups: input.groups } : { groups: [input as StrategyPerformance] };
}

export async function listRunningStrategies(params: RunningStrategyFilter = {}): Promise<PageResult<RunningStrategySummary>> {
  const rsp = await callControl<
    { page: PageRequest; space_id?: string; status?: string; mode?: string; strategy_id?: string },
    { items?: RunningStrategySummary[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListRunningStrategies", {
    page: { page: params.page ?? 1, page_size: params.page_size ?? 20 },
    space_id: params.space_id,
    status: params.status,
    mode: params.mode,
    strategy_id: params.strategy_id
  });
  return { items: rsp.items ?? [], page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size } };
}

export async function getStrategyOverview(binding_id: string): Promise<StrategyOverview> {
  return callControl("strategy", "GetStrategyOverview", { binding_id });
}

export async function listStrategyRuns(
  binding_id: string,
  params: PageRequest & { from?: string; to?: string } = {}
): Promise<PageResult<StrategyRun>> {
  const rsp = await callControl<
    { binding_id: string; page: PageRequest; range: { from: string; to: string } },
    { items?: StrategyRun[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyRuns", {
    binding_id,
    page: { page: params.page ?? 1, page_size: params.page_size ?? 20 },
    range: { from: params.from ?? "", to: params.to ?? "" }
  });
  return { items: rsp.items ?? [], page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size } };
}

export async function listStrategyTargets(run_id: string, params: PageRequest = {}): Promise<PageResult<TargetWeight>> {
  const rsp = await callControl<
    { run_id: string; page: PageRequest },
    { targets?: TargetWeight[]; total?: number; page?: number; page_size?: number }
  >("strategy", "ListStrategyTargets", { run_id, page: { page: params.page ?? 1, page_size: params.page_size ?? 100 } });
  return { items: rsp.targets ?? [], page: { total: rsp.total ?? 0, page: rsp.page, page_size: rsp.page_size } };
}

export async function getStrategyHealth(binding_id: string): Promise<StrategyHealth> {
  const rsp = await callControl<{ binding_id: string }, { health?: StrategyHealth }>("strategy", "GetStrategyHealth", {
    binding_id
  });
  return rsp.health ?? { status: "unknown", mode: "observe" };
}

export async function getStrategyPerformance(
  binding_id: string,
  source: PerformanceSource,
  params: { from?: string; to?: string; interval?: string } = {}
): Promise<StrategyPerformance> {
  const rsp = await callControl<
    { binding_id: string; performance_source: PerformanceSource; interval: string; range: { from: string; to: string } },
    StrategyPerformance
  >("strategy", "GetStrategyPerformance", {
    binding_id,
    performance_source: source,
    interval: params.interval ?? "auto",
    range: { from: params.from ?? "", to: params.to ?? "" }
  });
  return { ...rsp, performance_source: rsp.performance_source || source, points: rsp.points || [] };
}

export async function pauseBinding(binding_id: string, reason: string, operation_id: string) {
  return callControl("strategy", "PauseBinding", { binding_id, reason, operation_id });
}

export async function resumeBinding(binding_id: string, reason: string, operation_id: string) {
  return callControl("strategy", "ResumeBinding", { binding_id, reason, operation_id });
}

export interface ExecutionSettings {
  channel_id?: string;
  capital_amount?: string;
  quote_asset?: string;
}

export async function setExecutionMode(
  binding_id: string,
  mode: string,
  reason: string,
  operation_id: string,
  settings: ExecutionSettings = {}
) {
  return callControl("strategy", "SetExecutionMode", { binding_id, mode, reason, operation_id, ...settings });
}
