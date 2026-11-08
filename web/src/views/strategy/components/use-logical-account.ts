import { ref, watch, type Ref } from "vue";
import { getLogicalAccount } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";

/**
 * 实例只存组合账户编号，页面要显示名称和暂停状态。读取失败时 error 写原因，页面要说明“读取失败”，
 * 不能当成没有账户（否则“组合账户已暂停”的提示也就不出现了）。reload 给刷新和重试用：
 * 重读时保留上次读到的账户，读到新的再替换。
 */
export function useLogicalAccount(id: Ref<string | undefined>) {
  const account = ref<LogicalAccount | null>(null);
  const error = ref("");

  async function load(value: string | undefined) {
    if (!value) return;
    try {
      const response = await getLogicalAccount(value);
      if (id.value !== value) return;
      account.value = response.logical_account;
      error.value = "";
    } catch (cause) {
      if (id.value === value) error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
    }
  }

  watch(
    id,
    value => {
      account.value = null;
      error.value = "";
      return load(value);
    },
    { immediate: true }
  );
  return { account, error, reload: () => load(id.value) };
}
