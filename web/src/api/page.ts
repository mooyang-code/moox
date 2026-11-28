export type TotalMode = "AUTO" | "NONE" | "FORCE_EXACT" | number;

export type TotalState = "UNKNOWN" | "EXACT" | "SKIPPED" | number;

/** 统一分页请求（对应 commonpb.Page）。 */
export interface Page {
  page?: number;
  size?: number;
  cursor?: string;
}

/** 统一分页响应（对应 commonpb.PageResult）。 */
export interface PageResult {
  page?: number;
  size?: number;
  total?: number;
  has_more?: boolean;
  next_cursor?: string;
  total_state?: TotalState;
}
