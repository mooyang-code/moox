import { callFactor } from "./http";
import type {
  EngineStatus,
  FactorCategory,
  FactorDef,
  FactorInput,
  FactorMember,
  FactorRetRsp,
  FactorSet,
  ListFactorSetsReq,
  ListFactorSetsRsp,
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
  SetFactorMemberStatusResult
} from "./types";

export type CreateFactorSetInput = Pick<FactorSet, "space_id" | "source_dataset_id" | "freq" | "subject_mode" | "subjects"> &
  Partial<Pick<FactorSet, "set_id" | "result_dataset_id" | "status">>;

export async function createFactorSet(factorSet: CreateFactorSetInput) {
  const rsp = await callFactor<{ factor_set: CreateFactorSetInput }, FactorRetRsp<{ factor_set: FactorSet }>>("CreateFactorSet", {
    factor_set: factorSet
  });
  return rsp.factor_set;
}

export function updateFactorSet(set_id: string, subject_mode: string, subjects: string[]) {
  return callFactor<{ set_id: string; subject_mode: string; subjects: string[] }, FactorRetRsp<{ factor_set: FactorSet }>>(
    "UpdateFactorSet",
    { set_id, subject_mode, subjects }
  );
}

export async function setFactorSetStatus(set_id: string, status: string) {
  const rsp = await callFactor<{ set_id: string; status: string }, FactorRetRsp<{ factor_set: FactorSet }>>(
    "SetFactorSetStatus",
    { set_id, status }
  );
  return rsp.factor_set;
}

export function deleteFactorSet(set_id: string, purge = false) {
  return callFactor<{ set_id: string; purge: boolean }, FactorRetRsp>("DeleteFactorSet", { set_id, purge });
}

export function listFactorSets(params: ListFactorSetsReq = {}) {
  return callFactor<ListFactorSetsReq, FactorRetRsp<ListFactorSetsRsp>>("ListFactorSets", params);
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

/** 把定义加入因子集；member_alias 缺省时取定义 ID（共享集合不需要别名）。 */
export async function addFactorToSet(set_id: string, factor_id: string, member_alias?: string) {
  const body: { set_id: string; factor_id: string; member_alias?: string } = { set_id, factor_id };
  if (member_alias && member_alias !== factor_id) body.member_alias = member_alias;
  const rsp = await callFactor<typeof body, FactorRetRsp<{ member: FactorMember }>>("AddFactorToSet", body);
  return rsp.member;
}

/** 成员以集合内唯一的别名标识（共享集合里别名就是定义 ID）。 */
export function removeFactorFromSet(set_id: string, member_alias: string) {
  return callFactor<{ set_id: string; member_alias: string }, FactorRetRsp>("RemoveFactorFromSet", { set_id, member_alias });
}

export async function setFactorMemberStatus(
  set_id: string,
  member_alias: string,
  status: string
): Promise<SetFactorMemberStatusResult> {
  const rsp = await callFactor<
    { set_id: string; member_alias: string; status: string },
    FactorRetRsp<SetFactorMemberStatusResult>
  >("SetFactorMemberStatus", { set_id, member_alias, status });
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

/** 读取因子集的冻结记录（每期实际用的参数）与可选的提交审计。 */
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

/** 在因子集的源数据上试算：少量标的、最近若干期，不写结果。 */
export function previewFactor(params: PreviewFactorReq) {
  return callFactor<PreviewFactorReq, PreviewFactorRsp>("PreviewFactor", params);
}

export function listFactorFunctions() {
  return callFactor<Record<string, never>, ListFactorFunctionsRsp>("ListFactorFunctions", {});
}
