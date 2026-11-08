import type { Secret } from "@/api/admin/secret";

/** 下架检测用的币安生产只读密钥：删掉或禁用后自动下架检测会停止（监控会报“缺少币安只读 Key”）。 */
export const DELIST_READONLY_SECRET_ID = "exchange-binance-production-readonly";

/**
 * 交易所密钥的影响：交易服务已经建好的会话继续用旧密钥，重建会话后执行账户才变成未就绪，
 * 所以要写清楚不会立即停，要立即停得暂停组合账户或停用执行账户。
 */
const EXCHANGE_EFFECT =
  "已运行的会话不受影响，交易服务重建会话后，用它的执行账户才变成未就绪、不能同步和下单；要立即停止，请先暂停组合账户或停用执行账户。";

function consequences(secret: Pick<Secret, "secret_id" | "category">, deleting: boolean): string {
  if (secret.secret_id === DELIST_READONLY_SECRET_ID) {
    // 采集只认这个固定 ID；删除是软删除，setup apply 不会重建，密钥页新录入的 ID 是随机的，所以删了目前补不回来
    return deleting
      ? "这是下架检测用的币安只读密钥。删除后目前补不回来（setup apply 不会重建，新录入的密钥 ID 对不上），自动下架检测会一直停止；只想暂停请用禁用。"
      : "这是下架检测用的币安只读密钥，禁用后自动下架检测会停止。";
  }
  if (secret.category === "exchange") return EXCHANGE_EFFECT;
  return "";
}

/** 删除确认：写出名称和影响。后端直接删除、不检查引用，所以影响要在这里说清楚。 */
export function secretDeleteText(secret: Pick<Secret, "secret_id" | "name" | "category">): string {
  return [`删除密钥「${secret.name}」？删除后无法恢复。`, consequences(secret, true)].filter(Boolean).join("");
}

/** 禁用、启用确认：禁用时写出影响。 */
export function secretToggleText(secret: Pick<Secret, "secret_id" | "name" | "category" | "status">): string {
  if (secret.status !== "active") return `启用密钥「${secret.name}」？`;
  return [`禁用密钥「${secret.name}」？`, consequences(secret, false)].filter(Boolean).join("");
}
