import { callFactor } from './http';
import type {
  EngineStatus,
  FactorBinding,
  FactorDef,
  FactorRetRsp,
  ListBindingsReq,
  ListBindingsRsp,
  ListFactorsReq,
  ListFactorsRsp,
  ListRunsReq,
  ListRunsRsp,
  RecalcFactorReq,
  RecalcProgress,
} from './types';

export async function createFactorDef(factor: FactorDef) {
  const rsp = await callFactor<{ factor: FactorDef }, FactorRetRsp<{ factor: FactorDef }>>('CreateFactor', { factor });
  return rsp.factor;
}

export async function updateFactorDef(factor: FactorDef) {
  const rsp = await callFactor<{ factor_id: string; factor: FactorDef }, FactorRetRsp<{ factor: FactorDef }>>('UpdateFactor', {
    factor_id: factor.factor_id,
    factor,
  });
  return rsp.factor;
}

export function listFactorDefs(params: ListFactorsReq) {
  return callFactor<ListFactorsReq, FactorRetRsp<ListFactorsRsp>>('ListFactors', params);
}

export async function setFactorStatus(factor_id: string, status: string) {
  const rsp = await callFactor<{ factor_id: string; status: string }, FactorRetRsp<{ factor: FactorDef }>>('SetFactorStatus', {
    factor_id,
    status,
  });
  return rsp.factor;
}

export async function upsertFactorBinding(binding: FactorBinding) {
  const rsp = await callFactor<{ binding: FactorBinding }, FactorRetRsp<{ binding: FactorBinding }>>('UpsertBinding', { binding });
  return rsp.binding;
}

export function listFactorBindings(params: ListBindingsReq) {
  return callFactor<ListBindingsReq, FactorRetRsp<ListBindingsRsp>>('ListBindings', params);
}

export function deleteFactorBinding(binding_id: string) {
  return callFactor<{ binding_id: string }, FactorRetRsp>('DeleteBinding', { binding_id });
}

export function listFactorRuns(params: ListRunsReq) {
  return callFactor<ListRunsReq, FactorRetRsp<ListRunsRsp>>('ListFactorRuns', params);
}

export function recalcFactor(params: RecalcFactorReq) {
  return callFactor<RecalcFactorReq, FactorRetRsp<{ recalc_id: string }>>('RecalcFactor', params);
}

export function getRecalcProgress(recalc_id: string) {
  return callFactor<{ recalc_id: string }, RecalcProgress>('GetRecalcProgress', { recalc_id });
}

export function getEngineStatus() {
  return callFactor<Record<string, never>, EngineStatus>('GetEngineStatus', {});
}
