import { listFunctionInstances } from "@/api/function-instance";

/**
 * 云账户是全局的，执行器实例按空间存放：逐个空间查用这个云账户的实例数再相加。
 * 没有空间列表或任一空间查询失败时返回 null，确认框只写一般影响，不说“没有实例在用”。
 */
export async function instancesUsingAccount(accountId: string, spaceIds: readonly string[]): Promise<number | null> {
  if (!spaceIds.length) return null;
  try {
    const totals = await Promise.all(
      spaceIds.map(spaceId => listFunctionInstances({ cloud_account_id: accountId, page_size: 1 }, spaceId))
    );
    return totals.reduce((sum, item) => sum + item.total, 0);
  } catch {
    return null;
  }
}

/**
 * 删除云账户的确认正文。后端是软删除、不检查引用：还在用它的执行器实例之后在管理台删不掉、
 * 不能部署和调用，云端函数也会留着，所以把在用的实例数（所有空间合计）写进正文。
 */
export function cloudAccountDeleteText(inUse: number | null): string {
  if (inUse && inUse > 0) {
    return `所有空间里还有 ${inUse} 个执行器实例在用这个云账户：删除后这些实例在管理台删不掉、不能部署和调用，云端函数会留着。请先删除这些实例。`;
  }
  if (inUse === 0) return "所有空间里都没有执行器实例在用这个云账户。";
  return "用这个云账户部署的执行器实例删除后在管理台删不掉、不能部署和调用，云端函数会留着。请先确认没有实例在用。";
}
