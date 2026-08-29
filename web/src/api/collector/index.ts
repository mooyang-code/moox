import { callControl } from "@/api/admin/http";
import type { ResampleBackfillSummary } from "@/views/collector/collector-rules/resample-backfill";

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

export async function getKlineResampleBackfillStatus(spaceId: string, ruleId: string, requestId = ""): Promise<ResampleBackfillSummary | null> {
  let response: {
    request_id?: string;
    start?: string;
    end?: string;
    next_bucket?: string;
    state?: ResampleBackfillSummary["state"];
    participants?: number;
  };
  try {
    response = await callControl<
      { space_id: string; rule_id: string; request_id?: string },
      typeof response
    >("collectmgr", "GetKlineResampleBackfill", { space_id: spaceId, rule_id: ruleId, request_id: requestId });
  } catch (error) {
    if (error instanceof Error && /backfill request not found/i.test(error.message)) return null;
    throw error;
  }
  if (!response.request_id) return null;
  if (["complete", "canceled", "failed"].includes(String(response.state || ""))) return null;
  return {
    requestId: String(response.request_id),
    start: String(response.start || ""),
    end: String(response.end || ""),
    nextBucket: String(response.next_bucket || ""),
    state: response.state || "running",
    participants: Number(response.participants || 0)
  };
}
