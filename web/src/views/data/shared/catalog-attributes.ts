import type { Subject, Tag } from "@/api/storage/types";

// Storage 只保存属性；标签与标的的业务含义由 Collector 约定，这里集中读取。

/** 标签属性键。 */
export const TAG_ATTR = {
  marketType: "market_type",
  syncSource: "sync.source",
  syncCron: "sync.cron",
  syncTimezone: "sync.timezone"
} as const;

export const DEFAULT_TAG_CRON = "0 * * * *";
export const DEFAULT_TAG_TIMEZONE = "UTC";

function attr(attributes: Record<string, string> | undefined, key: string): string {
  return (attributes?.[key] ?? "").trim();
}

export function tagSource(tag: Pick<Tag, "attributes">): string {
  return attr(tag.attributes, TAG_ATTR.syncSource);
}

export function tagMarketType(tag: Pick<Tag, "attributes">): string {
  return attr(tag.attributes, TAG_ATTR.marketType);
}

export function tagCron(tag: Pick<Tag, "attributes">): string {
  return attr(tag.attributes, TAG_ATTR.syncCron) || DEFAULT_TAG_CRON;
}

export function tagTimezone(tag: Pick<Tag, "attributes">): string {
  return attr(tag.attributes, TAG_ATTR.syncTimezone) || DEFAULT_TAG_TIMEZONE;
}

export function subjectType(subject: Pick<Subject, "attributes"> | undefined): string {
  return attr(subject?.attributes, "subject_type");
}

export function subjectMarket(subject: Pick<Subject, "attributes"> | undefined): string {
  return attr(subject?.attributes, "market");
}

export function subjectCurrency(subject: Pick<Subject, "attributes"> | undefined): string {
  return attr(subject?.attributes, "currency");
}

/** 把表单里的业务属性合并进已有属性：空值删除该键，其余键原样保留。 */
export function mergeAttributes(base: Record<string, string> | undefined, patch: Record<string, string>): Record<string, string> {
  const merged: Record<string, string> = { ...(base || {}) };
  for (const [key, value] of Object.entries(patch)) {
    const text = value.trim();
    if (text) merged[key] = text;
    else delete merged[key];
  }
  return merged;
}
