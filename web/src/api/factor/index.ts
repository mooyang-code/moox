import { callFactor } from "./http";
import type {
  EngineStatus,
  FactorCategory,
  FactorDef,
  FactorInput,
  FactorMember,
  FactorRetRsp,
  MemberKind,
  FactorTask,
  ListFactorTasksReq,
  ListFactorTasksRsp,
  ListFactorsReq,
  ListFactorsRsp,
  GetFactorRsp,
  GetFactorParamsReq,
  GetFactorParamsRsp,
  ListRecalcJobsReq,
  ListRecalcJobsRsp,
  ListFactorFunctionsRsp,
  PreviewFactorReq,
  PreviewFactorRsp,
  RecalcFactorsReq,
  ValidateFactorSpecRsp,
  RecalcJob,
  SetTaskMemberStatusResult
} from "./types";

export type CreateFactorTaskInput = Pick<FactorTask, "space_id" | "source_dataset_id" | "freq" | "name"> &
  Partial<Pick<FactorTask, "task_id" | "result_dataset_id" | "status">>;

export async function createFactorTask(factorTask: CreateFactorTaskInput) {
  const rsp = await callFactor<{ factor_task: CreateFactorTaskInput }, FactorRetRsp<{ factor_task: FactorTask }>>(
    "CreateFactorTask",
    {
      factor_task: factorTask
    }
  );
  return rsp.factor_task;
}

export async function setFactorTaskStatus(task_id: string, status: string) {
  const rsp = await callFactor<{ task_id: string; status: string }, FactorRetRsp<{ factor_task: FactorTask }>>(
    "SetFactorTaskStatus",
    { task_id, status }
  );
  return rsp.factor_task;
}

export function deleteFactorTask(task_id: string, purge = false) {
  return callFactor<{ task_id: string; purge: boolean }, FactorRetRsp>("DeleteFactorTask", { task_id, purge });
}

export function listFactorTasks(params: ListFactorTasksReq = {}) {
  return callFactor<ListFactorTasksReq, FactorRetRsp<ListFactorTasksRsp>>("ListFactorTasks", params);
}

export async function createFactor(factor: FactorInput) {
  const rsp = await callFactor<{ factor: FactorInput }, FactorRetRsp<{ factor: FactorDef }>>("CreateFactor", { factor });
  return rsp.factor;
}

export async function updateFactor(factor: FactorInput) {
  const rsp = await callFactor<{ factor: FactorInput }, FactorRetRsp<{ factor: FactorDef }>>("UpdateFactor", { factor });
  return rsp.factor;
}

export function listFactors(params: ListFactorsReq = {}) {
  return callFactor<ListFactorsReq, FactorRetRsp<ListFactorsRsp>>("ListFactors", params);
}

/**
 * 把定义加入计算任务。kind 缺省按 fixed；params 是固定成员的固定参数、或可变成员的默认参数（没给的参数取定义的默认值，
 * 加成员之后不再改）。成员键由服务端推导并在响应里返回。
 */
export async function addFactorToTask(
  task_id: string,
  factor_id: string,
  kind: MemberKind = "fixed",
  params?: Record<string, number>
) {
  const body: { task_id: string; factor_id: string; kind: MemberKind; params?: Record<string, number> } = {
    task_id,
    factor_id,
    kind
  };
  if (params && Object.keys(params).length) body.params = params;
  const rsp = await callFactor<typeof body, FactorRetRsp<{ member: FactorMember }>>("AddTaskMember", body);
  return rsp.member;
}

/** 成员以任务内唯一的成员键标识。 */
export function removeFactorFromTask(task_id: string, member_key: string) {
  return callFactor<{ task_id: string; member_key: string }, FactorRetRsp>("RemoveTaskMember", { task_id, member_key });
}

export async function setFactorMemberStatus(
  task_id: string,
  member_key: string,
  status: string
): Promise<SetTaskMemberStatusResult> {
  const rsp = await callFactor<{ task_id: string; member_key: string; status: string }, FactorRetRsp<SetTaskMemberStatusResult>>(
    "SetTaskMemberStatus",
    { task_id, member_key, status }
  );
  return { member: rsp.member };
}

export async function listFactorCategories() {
  const rsp = await callFactor<Record<string, never>, FactorRetRsp<{ categories: FactorCategory[] }>>("ListFactorCategories", {});
  return rsp.categories || [];
}

export async function upsertFactorCategory(category: Pick<FactorCategory, "category_id" | "name"> & { sort_order?: number }) {
  const rsp = await callFactor<{ category: typeof category }, FactorRetRsp<{ category: FactorCategory }>>(
    "UpsertFactorCategory",
    {
      category
    }
  );
  return rsp.category;
}

export function deleteFactorCategory(category_id: string) {
  return callFactor<{ category_id: string }, FactorRetRsp>("DeleteFactorCategory", { category_id });
}

/** 修改定义的分类（分类只是元数据，不进 definition_hash，不影响已启用的成员）。 */
export async function setFactorCategory(factor_id: string, category_id: string) {
  const rsp = await callFactor<{ factor_id: string; category_id: string }, FactorRetRsp<{ factor: FactorDef }>>(
    "SetFactorCategory",
    {
      factor_id,
      category_id
    }
  );
  return rsp.factor;
}

/** 读取因子任务里各可变成员最近一次被接受的提交（只读）。 */
export function getFactorParams(params: GetFactorParamsReq) {
  return callFactor<GetFactorParamsReq, FactorRetRsp<GetFactorParamsRsp>>("GetFactorParams", params);
}

export function deleteFactor(factor_id: string) {
  return callFactor<{ factor_id: string }, FactorRetRsp>("DeleteFactor", { factor_id });
}

export function getFactor(factor_id: string) {
  return callFactor<{ factor_id: string }, FactorRetRsp<GetFactorRsp>>("GetFactor", { factor_id });
}

export async function recalcFactors(params: RecalcFactorsReq) {
  const rsp = await callFactor<RecalcFactorsReq, FactorRetRsp<{ job: RecalcJob }>>("RecalcFactors", params);
  return rsp.job;
}

export async function cancelRecalcJob(job_id: string) {
  const rsp = await callFactor<{ job_id: string }, FactorRetRsp<{ job: RecalcJob }>>("CancelRecalcJob", { job_id });
  return rsp.job;
}

export function listRecalcJobs(params: ListRecalcJobsReq) {
  return callFactor<ListRecalcJobsReq, FactorRetRsp<ListRecalcJobsRsp>>("ListRecalcJobs", params);
}

export function getFactorStatus() {
  return callFactor<Record<string, never>, EngineStatus>("GetStatus", {});
}

/** 编译一个定义：定义不合法时 valid=false，诊断带字段路径与行列（不是接口错误）。 */
export function validateFactorSpec(spec: string, allow_partial_universe = false) {
  return callFactor<{ spec: string; allow_partial_universe: boolean }, ValidateFactorSpecRsp>("ValidateFactorSpec", {
    spec,
    allow_partial_universe
  });
}

/** 在因子任务的源数据上试算：少量标的、最近若干期，不写结果。 */
export function previewFactor(params: PreviewFactorReq) {
  return callFactor<PreviewFactorReq, PreviewFactorRsp>("PreviewFactor", params);
}

export function listFactorFunctions() {
  return callFactor<Record<string, never>, ListFactorFunctionsRsp>("ListFactorFunctions", {});
}
