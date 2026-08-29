import { callControl } from "@/api/admin/http";
import { summarizeBackfillResults, type ResampleBackfillSummary } from "@/views/collector/collector-rules/resample-backfill";

export interface KlineResampleBackfillRequest {
  space_id: string;
  rule_id: string;
  request_id: string;
  start: string;
  end: string;
}

export async function startKlineResampleBackfill(request: KlineResampleBackfillRequest) {
  return callControl<KlineResampleBackfillRequest, Record<string, unknown>>(
    "collectmgr",
    "StartKlineResampleBackfill",
    request
  );
}

export async function cancelKlineResampleBackfill(request: Pick<KlineResampleBackfillRequest, "space_id" | "rule_id" | "request_id">) {
  return callControl<typeof request, Record<string, unknown>>("collectmgr", "CancelKlineResampleBackfill", request);
}

export async function getKlineResampleBackfillStatus(spaceId: string, ruleId: string): Promise<ResampleBackfillSummary | null> {
  const instances: Array<Record<string, any>> = [];
  for (let page = 1; page <= 100; page += 1) {
    const response = await callControl<
      { filter: Record<string, unknown> },
      { instances?: Array<Record<string, any>>; page?: { has_more?: boolean } }
    >("collectmgr", "GetTaskInstanceList", {
      filter: { space_id: spaceId, rule_id: ruleId, data_type: "kline_resample", page: { page, size: 500 } }
    });
    const rows = response.instances || [];
    instances.push(...rows);
    if (!response.page?.has_more || rows.length === 0) break;
  }
  return summarizeBackfillResults(instances);
}
