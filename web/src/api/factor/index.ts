import { callFactor } from "./http";
import type {
  EngineStatus,
  FactorDef,
  FactorRetRsp,
  FactorSet,
  FactorSetInfo,
  ListFactorSetsReq,
  ListFactorSetsRsp,
  ListFactorsReq,
  ListFactorsRsp,
  ListRecalcJobsReq,
  ListRecalcJobsRsp,
  RecalcFactorsReq,
  RecalcJob,
  SetFactorStatusResult
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

export function getFactorSet(set_id: string) {
  return callFactor<{ set_id: string }, FactorRetRsp<{ factor_set: FactorSet; factors: FactorDef[]; last_run?: FactorSetInfo["last_run"] }>>(
    "GetFactorSet",
    { set_id }
  );
}

export function listFactorSets(params: ListFactorSetsReq = {}) {
  return callFactor<ListFactorSetsReq, FactorRetRsp<ListFactorSetsRsp>>("ListFactorSets", params);
}

export async function createFactor(factor: FactorDef) {
  const rsp = await callFactor<{ factor: FactorDef }, FactorRetRsp<{ factor: FactorDef }>>("CreateFactor", { factor });
  return rsp.factor;
}

export async function updateFactor(factor: FactorDef) {
  const rsp = await callFactor<{ factor: FactorDef }, FactorRetRsp<{ factor: FactorDef }>>("UpdateFactor", { factor });
  return rsp.factor;
}

export function listFactors(params: ListFactorsReq) {
  return callFactor<ListFactorsReq, FactorRetRsp<ListFactorsRsp>>("ListFactors", params);
}

export async function setFactorStatus(factor_id: string, status: string): Promise<SetFactorStatusResult> {
  const rsp = await callFactor<{ factor_id: string; status: string }, FactorRetRsp<SetFactorStatusResult>>("SetFactorStatus", {
    factor_id,
    status
  });
  return { factor: rsp.factor, backfill_job: rsp.backfill_job };
}

export function deleteFactor(factor_id: string) {
  return callFactor<{ factor_id: string }, FactorRetRsp>("DeleteFactor", { factor_id });
}

export function getFactor(factor_id: string) {
  return callFactor<{ factor_id: string }, FactorRetRsp<{ factor: FactorDef }>>("GetFactor", { factor_id });
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

export async function getRecalcJob(job_id: string) {
  const rsp = await callFactor<{ job_id: string }, FactorRetRsp<{ job: RecalcJob }>>("GetRecalcJob", { job_id });
  return rsp.job;
}

export function getFactorStatus() {
  return callFactor<Record<string, never>, EngineStatus>("GetStatus", {});
}
