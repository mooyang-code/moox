import { CronExpressionParser } from "cron-parser";
import type { Tag } from "@/api/storage/types";
import {
  DEFAULT_TAG_CRON,
  DEFAULT_TAG_TIMEZONE,
  TAG_ATTR,
  mergeAttributes,
  tagCron,
  tagMarketType,
  tagSource,
  tagTimezone
} from "@/views/data/shared/catalog-attributes";

export interface TagFormState {
  tag_id: string;
  tag_name: string;
  description: string;
  mode: "auto" | "manual";
  source: string;
  market_type: string;
  cron: string;
  timezone: string;
}

export const TAG_ID_PATTERN = /^[a-z][a-z0-9_]{0,63}$/;

export function validateTagForm(state: TagFormState, creating: boolean): string | undefined {
  if (creating && !TAG_ID_PATTERN.test(state.tag_id.trim())) return "标签 ID 须为小写字母开头的 snake_case";
  if (!state.tag_name.trim()) return "请输入标签名称";
  if (!state.source.trim() || !state.market_type) return "请选择数据源与市场类型";
  if (state.mode === "auto" && nextRuns(state.cron, state.timezone, 1).length === 0) return "定时更新表达式无效";
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

/**
 * 生成保存用的标签。来源、市场类型、cron、时区都写进 attributes；
 * 编辑时传入原标签，其余未知属性原样保留；手工标签不参与定时同步，不写 cron 与时区。
 */
export function toTagPayload(spaceId: string, state: TagFormState, existing?: Tag): Tag {
  const auto = state.mode === "auto";
  return {
    space_id: spaceId,
    tag_id: state.tag_id.trim(),
    tag_name: state.tag_name.trim(),
    description: state.description.trim(),
    mode: state.mode,
    attributes: mergeAttributes(existing?.attributes, {
      [TAG_ATTR.syncSource]: state.source,
      [TAG_ATTR.marketType]: state.market_type,
      [TAG_ATTR.syncCron]: auto ? state.cron.trim() || DEFAULT_TAG_CRON : "",
      [TAG_ATTR.syncTimezone]: auto ? state.timezone.trim() || DEFAULT_TAG_TIMEZONE : ""
    })
  };
}

export function tagToFormState(tag: Tag): TagFormState {
  return {
    tag_id: tag.tag_id,
    tag_name: tag.tag_name,
    description: tag.description || "",
    mode: tag.mode,
    source: tagSource(tag),
    market_type: tagMarketType(tag),
    cron: tagCron(tag),
    timezone: tagTimezone(tag)
  };
}
