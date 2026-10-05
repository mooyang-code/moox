import { callFactor } from "./http";
import type {
  EngineStatus,
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
  ListRecalcJobsReq,
  ListRecalcJobsRsp,
  RecalcFactorsReq,
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

export async function addFactorToSet(set_id: string, factor_id: string) {
  const rsp = await callFactor<{ set_id: string; factor_id: string }, FactorRetRsp<{ member: FactorMember }>>("AddFactorToSet", {
    set_id,
    factor_id
  });
  return rsp.member;
}

export function removeFactorFromSet(set_id: string, factor_id: string) {
  return callFactor<{ set_id: string; factor_id: string }, FactorRetRsp>("RemoveFactorFromSet", { set_id, factor_id });
}

export async function setFactorMemberStatus(
  set_id: string,
  factor_id: string,
  status: string
): Promise<SetFactorMemberStatusResult> {
  const rsp = await callFactor<{ set_id: string; factor_id: string; status: string }, FactorRetRsp<SetFactorMemberStatusResult>>(
    "SetFactorMemberStatus",
    { set_id, factor_id, status }
  );
  return { member: rsp.member, backfill_job: rsp.backfill_job };
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
