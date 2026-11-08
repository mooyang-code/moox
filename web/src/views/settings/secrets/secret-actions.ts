import type { Secret } from "@/api/admin/secret";

/** 下架检测用的币安生产只读密钥：删掉或禁用后自动下架检测会停止（监控会报“缺少币安只读 Key”）。 */
export const DELIST_READONLY_SECRET_ID = "exchange-binance-production-readonly";

function consequences(secret: Pick<Secret, "secret_id" | "category">): string {
  if (secret.secret_id === DELIST_READONLY_SECRET_ID) return "这是下架检测用的币安只读密钥，自动下架检测会停止。";
  if (secret.category === "exchange") return "正在使用它的执行账户会变成未就绪，不能同步和下单。";
  return "";
}

/** 删除确认：写出名称和影响。后端直接删除、不检查引用，所以影响要在这里说清楚。 */
export function secretDeleteText(secret: Pick<Secret, "secret_id" | "name" | "category">): string {
  return [`删除密钥「${secret.name}」？删除后无法恢复。`, consequences(secret)].filter(Boolean).join("");
}

/** 禁用、启用确认：禁用时写出影响。 */
export function secretToggleText(secret: Pick<Secret, "secret_id" | "name" | "category" | "status">): string {
  if (secret.status !== "active") return `启用密钥「${secret.name}」？`;
  return [`禁用密钥「${secret.name}」？`, consequences(secret)].filter(Boolean).join("");
}
