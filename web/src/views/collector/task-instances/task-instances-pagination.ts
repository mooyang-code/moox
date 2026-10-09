export interface TaskInstancePageResult {
  total?: number;
  has_more?: boolean;
  total_state?: number | string;
}

export function taskInstancePaginationTotal(
  page: number,
  pageSize: number,
  itemCount: number,
  pageResult?: TaskInstancePageResult
): number {
  const totalState = pageResult?.total_state;
  const skipped = totalState === 2 || totalState === "SKIPPED" || totalState === "TOTAL_STATE_SKIPPED";
  if (skipped) {
    return Math.max(0, (page - 1) * pageSize + itemCount + (pageResult?.has_more ? 1 : 0));
  }
  return Number(pageResult?.total) || itemCount;
}
