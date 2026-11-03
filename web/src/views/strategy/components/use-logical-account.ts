import { ref, watch, type Ref } from "vue";
import { getLogicalAccount } from "@/api/trade";
import type { LogicalAccount } from "@/api/trade/types";

// 实例只存组合账户编号，页面要显示名称和暂停状态。读取失败时退回显示编号。
export function useLogicalAccount(id: Ref<string | undefined>) {
  const account = ref<LogicalAccount | null>(null);
  watch(
    id,
    async value => {
      account.value = null;
      if (!value) return;
      try {
        const response = await getLogicalAccount(value);
        if (id.value === value) account.value = response.logical_account;
      } catch {
        account.value = null;
      }
    },
    { immediate: true }
  );
  return account;
}
