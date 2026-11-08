import { computed, type ComputedRef } from "vue";
import { useSpaceStore } from "@/store/modules/space";
import { resolveZone } from "@/utils/time-zone";

/**
 * 页面的显示时区。space：跟空间有关的页面，按当前空间登记的时区（空或非法时 UTC），切空间后跟着变；
 * global：和空间无关的页面（运维、主机、密钥、空间列表、事件中心等），固定 UTC。
 * 可被多处复用的组件（操作记录、检测结果、数据浏览）不要自己调它，由调用方把时区传进去。
 */
export function useDisplayZone(scope: "space" | "global" = "space"): ComputedRef<string> {
  const spaceStore = useSpaceStore();
  return computed(() => (scope === "global" ? "UTC" : resolveZone(spaceStore.selectedSpace?.timezone)));
}
