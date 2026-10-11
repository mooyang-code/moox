import { computed, ref } from "vue";
import { defineStore } from "pinia";
import { listSpaces } from "@/api/admin/spaces";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";
import type { Space } from "@/api/admin/types";

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
        spaces.value = rsp.spaces || [];
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

    return { spaces, selectedSpaceId, selectedSpace, loading, loadSpaces, setSelectedSpace, hasSpace, requireSpaceId };
  },
  { persist: true }
);
