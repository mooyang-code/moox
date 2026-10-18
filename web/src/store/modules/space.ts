import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { listSpaces } from "@/api/admin/spaces";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import { setSpaceInitializer } from "@/api/admin/signed-client";
import type { Space } from "@/api/admin/types";

/**
 * 系统内部空间：以 mooxsys 开头的空间存放主机指标、服务指标及其视图，不是交易市场，
 * 不出现在全局空间选择里，也不能设为当前空间。
 */
export function isSystemSpace(spaceId: string | undefined): boolean {
  return (spaceId || "").trim().toLowerCase().startsWith("mooxsys");
}

export const useSpaceStore = defineStore(
  "spaceStore",
  () => {
    const spaces = ref<Space[]>([]);
    const selectedSpaceId = ref<string>("");
    const loading = ref(false);

    const selectedSpace = computed(() => spaces.value.find(item => item.space_id === selectedSpaceId.value));

    // 并发调用共用同一个在途请求：后到的调用者也必须等到空间真正初始化完成，
    // 否则随后的请求拿不到空间头会被 Console 拒绝。
    let inflight: Promise<void> | null = null;

    function loadSpaces(): Promise<void> {
      if (!inflight) {
        inflight = fetchSpaces().finally(() => {
          inflight = null;
        });
      }
      return inflight;
    }

    async function fetchSpaces() {
      loading.value = true;
      try {
        const rsp = await listSpaces({ status: "active", page: { page: 1, size: 200 } });
        spaces.value = (rsp.spaces || []).filter(space => !isSystemSpace(space.space_id));
        if (!selectedSpaceId.value && spaces.value.length > 0) {
          selectedSpaceId.value = spaces.value[0].space_id;
        }
        if (selectedSpaceId.value && !spaces.value.some(item => item.space_id === selectedSpaceId.value)) {
          selectedSpaceId.value = spaces.value[0]?.space_id || "";
        }
        setSelectedSpaceIdCache(selectedSpaceId.value);
      } finally {
        loading.value = false;
      }
    }

    function setSelectedSpace(spaceId: string) {
      if (!hasSpace(spaceId)) return;
      selectedSpaceId.value = spaceId;
      setSelectedSpaceIdCache(spaceId);
    }

    function hasSpace(spaceId: string) {
      return Boolean(spaceId) && spaces.value.some(item => item.space_id === spaceId);
    }

    function requireSpaceId() {
      if (!selectedSpaceId.value) throw new Error("请先选择空间");
      return selectedSpaceId.value;
    }

    // 登记给请求层：没有已选空间时，空间内的请求先等空间列表加载完成。
    setSpaceInitializer(loadSpaces);

    return { spaces, selectedSpaceId, selectedSpace, loading, loadSpaces, setSelectedSpace, hasSpace, requireSpaceId };
  },
  { persist: true }
);
