import type { Secret } from "@/api/admin/secret";
import { isReadonlySecret, secretEnvironmentOf } from "@/components/trade-inspection/inspection-request";

/**
 * 交易所密钥的影响：交易服务已经建好的会话继续用旧密钥，重建会话后执行账户才变成未就绪，
 * 所以要写清楚不会立即停，要立即停得暂停组合账户或停用执行账户。
 */
const EXCHANGE_EFFECT =
  "已运行的会话不受影响，交易服务重建会话后，用它的执行账户才变成未就绪、不能同步和下单；要立即停止，请先暂停组合账户或停用执行账户。";

/** 判断是否是 Collector 下架检测用的币安生产环境只读密钥（缺省环境视同生产环境）。 */
export function isDelistReadonlySecret(secret: Pick<Secret, "category" | "provider" | "extra_config">): boolean {
  return (
    secret.category === "exchange" &&
    (secret.provider ?? "").trim().toLowerCase() === "binance" &&
    isReadonlySecret(secret.extra_config) &&
    secretEnvironmentOf(secret.extra_config) !== 1
  );
}

function consequences(secret: Pick<Secret, "category" | "provider" | "extra_config">, deleting: boolean): string {
  if (isDelistReadonlySecret(secret)) {
    return deleting
      ? "这是下架检测用的币安生产只读密钥。删除后如果没有其他启用的币安生产只读密钥，自动下架检测会停止（可随时在密钥管理页重新新建）；只想暂停请用禁用。"
      : "这是下架检测用的币安生产只读密钥，禁用后如果没有其他启用的币安生产只读密钥，自动下架检测会停止。";
  }
  if (secret.category === "exchange") return EXCHANGE_EFFECT;
  return "";
}

/** 删除确认：写出名称和影响。后端直接删除、不检查引用，所以影响要在这里说清楚。 */
export function secretDeleteText(secret: Pick<Secret, "secret_id" | "name" | "category" | "provider" | "extra_config">): string {
  return [`删除密钥「${secret.name}」？删除后无法恢复。`, consequences(secret, true)].filter(Boolean).join("");
}

/** 禁用、启用确认：禁用时写出影响。 */
export function secretToggleText(
  secret: Pick<Secret, "secret_id" | "name" | "category" | "provider" | "extra_config" | "status">
): string {
  if (secret.status !== "active") return `启用密钥「${secret.name}」？`;
  return [`禁用密钥「${secret.name}」？`, consequences(secret, false)].filter(Boolean).join("");
}
