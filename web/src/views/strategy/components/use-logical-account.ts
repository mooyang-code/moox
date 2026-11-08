import { ref, watch, type Ref } from "vue";
import { getLogicalAccount } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";

/**
 * 实例只存组合账户编号，页面要显示名称和暂停状态。读取失败时 error 写原因，页面要说明“读取失败”，
 * 不能当成没有账户（否则“组合账户已暂停”的提示也就不出现了）。
 */
export function useLogicalAccount(id: Ref<string | undefined>) {
  const account = ref<LogicalAccount | null>(null);
  const error = ref("");
  watch(
    id,
    async value => {
      account.value = null;
      error.value = "";
      if (!value) return;
      try {
        const response = await getLogicalAccount(value);
        if (id.value === value) account.value = response.logical_account;
      } catch (cause) {
        if (id.value === value) error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
      }
    },
    { immediate: true }
  );
  return { account, error };
}
