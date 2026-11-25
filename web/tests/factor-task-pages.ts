import { readAllPages } from "@/api/read-all-pages";

type ListFactorTasks = (req: { page: { page: number; size: number } }) => Promise<{
  factor_tasks?: unknown[];
  page_result?: { has_more?: boolean };
}>;

/** 测试里用：按 mock 的 listFactorTasks 翻页，得到与 api/factor 的 listAllFactorTasks 相同的结果。 */
export function allFactorTasksFrom(listFactorTasks: ListFactorTasks) {
  return () =>
    readAllPages(async page => {
      const rsp = await listFactorTasks({ page: { page, size: 200 } });
      return { items: rsp.factor_tasks || [], hasMore: Boolean(rsp.page_result?.has_more) };
    });
}
