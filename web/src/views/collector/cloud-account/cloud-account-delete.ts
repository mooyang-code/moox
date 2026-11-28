/**
 * 删除云账号的确认正文。Collector 在账号还被 scf_plan 引用、还有代码包或云上还有它建的函数时会拒绝，
 * 所以前端不再自己检查，被拒绝时显示 Collector 给的原因。
 */
export const CLOUD_ACCOUNT_DELETE_TEXT =
  "只删除这条登记，不会删除云上的函数。账号还被 scf_plan 引用、还有代码包，或配置过的地域里还有它建的函数时，Collector 会拒绝并说明原因。" +
  "不再被 scf_plan 引用的旧账号，名下的函数 Collector 管不到，moox-cli collector function delete 也删不到：" +
  "请用 moox-cli collector function inspect 查出这些函数，在腾讯云控制台手工删除后再删账号。";

/** 删除失败的提示：优先用 Collector 返回的原因。 */
export function cloudAccountDeleteError(error: unknown): string {
  const reason = error instanceof Error && error.message ? error.message : "未知原因";
  return `删除云账号失败：${reason}`;
}
