import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import { listSpaces } from '@/api/admin/spaces';
import type { Space } from '@/api/admin/types';

export const useSpaceStore = defineStore(
  'spaceStore',
  () => {
    const spaces = ref<Space[]>([]);
    const selectedSpaceId = ref<string>('');
    const loading = ref(false);

    const selectedSpace = computed(() => spaces.value.find((item) => item.space_id === selectedSpaceId.value));

    async function loadSpaces() {
      if (loading.value) return;
      loading.value = true;
      try {
        const rsp = await listSpaces({ page: { page: 1, size: 200 } });
        spaces.value = rsp.spaces || [];
        if (!selectedSpaceId.value && spaces.value.length > 0) {
          selectedSpaceId.value = spaces.value[0].space_id;
        }
        if (selectedSpaceId.value && !spaces.value.some((item) => item.space_id === selectedSpaceId.value)) {
          selectedSpaceId.value = spaces.value[0]?.space_id || '';
        }
      } finally {
        loading.value = false;
      }
    }

    function setSelectedSpace(spaceId: string) {
      selectedSpaceId.value = spaceId;
    }

    function requireSpaceId() {
      if (!selectedSpaceId.value) throw new Error('请先选择空间');
      return selectedSpaceId.value;
    }

    return { spaces, selectedSpaceId, selectedSpace, loading, loadSpaces, setSelectedSpace, requireSpaceId };
  },
  { persist: true },
);
