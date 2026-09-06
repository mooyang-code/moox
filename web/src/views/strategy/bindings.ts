import type { FactorBinding, FactorDef } from "@/api/factor/types";
import type { View, ViewColumn } from "@/api/storage/types";

export interface BindingSelection {
  factor: FactorDef;
  binding: FactorBinding;
  output: string;
  column_name: string;
}

export function validateAliasConflicts(selections: BindingSelection[]): string | null {
  const owners = new Map<string, number>();
  const sourceFields = new Set(selections.flatMap(selection => selection.factor.input_columns || []).map(value => value.trim()).filter(Boolean));
  const reserved = new Set(["open", "high", "low", "close", "volume", "instrument_id", "score"]);
  for (const [index, selection] of selections.entries()) {
    for (const raw of [selection.factor.factor_id, selection.output, selection.column_name]) {
      const name = raw.trim();
      if (!name) continue;
      if (reserved.has(name)) return `因子别名 ${name} 与内置字段冲突`;
      if (sourceFields.has(name)) return `因子别名 ${name} 与源字段冲突`;
      const owner = owners.get(name);
      if (owner !== undefined && owner !== index) return `因子别名 ${name} 被多个因子使用`;
      owners.set(name, index);
    }
  }
  return null;
}

/** Keep the UI comparison aligned with the backend's accepted frequency form. */
export function normalizeFrequency(value: string): string {
  const normalized = value.trim();
  const match = normalized.match(/^(\d+)([mhd])$/i);
  if (!match) return normalized;
  const unit = match[2] === "m" ? "m" : match[2].toUpperCase();
  return `${match[1]}${unit}`;
}

export function findOutputColumn(columns: ViewColumn[], factorId: string, output: string): ViewColumn | null {
  return (
    columns.find(column => column.attributes?.origin_factor_id === factorId && column.attributes?.factor_output === output) ?? null
  );
}

export function validBindings(bindings: FactorBinding[], sourceViewId: string, frequency: string): FactorBinding[] {
  const normalizedFrequency = normalizeFrequency(frequency);
  return bindings.filter(binding => {
    const bindingFrequency = binding.freq ? normalizeFrequency(binding.freq) : "";
    return binding.status === "enabled" && binding.source_view_id === sourceViewId && (!bindingFrequency || bindingFrequency === normalizedFrequency);
  });
}

export function canCombineSelections(selections: BindingSelection[], sourceView: View): { ok: boolean; reason?: string } {
  if (!selections.length) return { ok: true };
  const resultViewIds = new Set(selections.map(selection => selection.binding.result_view_id).filter(Boolean));
  if (resultViewIds.size !== 1) return { ok: false, reason: "所有因子必须共用一个结果 View" };
  if (resultViewIds.has(sourceView.view_id)) return { ok: false, reason: "因子结果 View 必须不同于源 View" };
  return { ok: true };
}

export function buildInputBindings(sourceView: View, frequency: string, selections: BindingSelection[]): string {
  const payload = {
    source_view_id: sourceView.view_id,
    frequency: normalizeFrequency(frequency),
    factors: selections.map(selection => ({
      factor_id: selection.factor.factor_id,
      source_hash: selection.factor.source_hash ?? "",
      input_columns: selection.factor.input_columns,
      params_json: selection.factor.params_json,
      lookback_periods: selection.factor.lookback_periods,
      binding_id: selection.binding.binding_id ?? "",
      frequency: normalizeFrequency(selection.binding.freq || frequency),
      result_dataset_id: selection.binding.result_dataset_id ?? "",
      result_view_id: selection.binding.result_view_id ?? "",
      output: selection.output,
      column_name: selection.column_name,
      subject_mode: selection.binding.subject_mode,
      subjects_json: selection.binding.subjects_json
    }))
  };
  return JSON.stringify(payload, null, 2);
}
