import { CronExpressionParser } from "cron-parser";
import type { Tag } from "@/api/storage/types";

export interface TagFormState {
  tag_id: string;
  tag_name: string;
  description: string;
  mode: "auto" | "manual";
  probe: boolean;
  sources: string[];
  instrument_type: string;
  cron: string;
  timezone: string;
}

export const TAG_ID_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;

export function validateTagForm(state: TagFormState, creating: boolean): string | undefined {
  if (creating && !TAG_ID_PATTERN.test(state.tag_id.trim())) return "标签 ID 须为小写字母开头的 snake_case";
  if (!state.tag_name.trim()) return "请输入标签名称";
  const needsSource = state.mode === "auto" || state.probe;
  if (needsSource && (state.sources.length === 0 || !state.instrument_type)) return "请选择数据源与产品类型";
  if (needsSource && nextRuns(state.cron, state.timezone, 1).length === 0) return "cron 表达式无效";
  return undefined;
}

export function nextRuns(cron: string, timezone: string, count = 3): string[] {
  if (count <= 0) return [];
  try {
    const iterator = CronExpressionParser.parse(cron, { tz: timezone });
    return Array.from({ length: count }, () => iterator.next().toISOString() || "");
  } catch {
    return [];
  }
}

export function toTagPayload(spaceId: string, state: TagFormState): Tag {
  const probe = state.mode === "auto" || state.probe;
  return {
    space_id: spaceId,
    tag_id: state.tag_id.trim(),
    tag_name: state.tag_name.trim(),
    description: state.description.trim(),
    mode: state.mode,
    sources: probe ? state.sources.map(item => item.trim()).filter(Boolean) : [],
    instrument_type: probe ? state.instrument_type : "",
    cron: state.cron.trim() || "0 * * * *",
    timezone: state.timezone.trim() || "UTC"
  };
}

export function tagToFormState(tag: Tag): TagFormState {
  return {
    tag_id: tag.tag_id,
    tag_name: tag.tag_name,
    description: tag.description || "",
    mode: tag.mode,
    probe: tag.mode === "auto" || Boolean(tag.sources?.length || tag.instrument_type),
    sources: tag.sources || [],
    instrument_type: tag.instrument_type || "",
    cron: tag.cron || "0 * * * *",
    timezone: tag.timezone || "UTC"
  };
}
