export type RouteQueryValue = string | string[] | null | undefined;
export type RouteQuery = Record<string, RouteQueryValue>;

export function getFunctionJobId(query: RouteQuery): string {
  const value = query.job_id;
  return Array.isArray(value) ? value[0] || "" : value || "";
}

export function setFunctionJobId(query: RouteQuery, jobId?: string): RouteQuery {
  const next = { ...query };
  if (jobId) {
    next.job_id = jobId;
  } else {
    delete next.job_id;
  }
  return next;
}
