<template>
  <div class="header_crumb">
    <ButtonCollapsed />
    <div class="space-selector">
      <a-select
        :model-value="selectedSpaceId"
        :options="spaceOptions"
        @change="onSpaceChange"
        placeholder="请选择空间"
        style="width: 220px; margin-left: 16px;"
        allow-search
        :loading="loading"
      >
        <template #empty>
          <div style="text-align: center; padding: 12px;">
            <div>暂无空间</div>
            <a-button type="text" size="small" @click="handleCreateSpace" style="margin-top: 8px;">
              创建空间
            </a-button>
          </div>
        </template>
      </a-select>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, computed } from 'vue';
import { useRouter } from 'vue-router';
import ButtonCollapsed from "@/layout/components/Header/components/button-collapsed/index.vue";
import { useSpaceStore } from '@/store/modules/space';
import { storeToRefs } from 'pinia';
import type { Space } from '@/api/control/types';

const router = useRouter();

const spaceStore = useSpaceStore();
const { spaces, selectedSpaceId, loading } = storeToRefs(spaceStore);

const spaceOptions = computed(() => {
  return spaces.value.map((space: Space) => ({
    label: space.name || space.space_id,
    value: space.space_id,
    disabled: false
  }));
});

const onSpaceChange = (spaceId: string | number | boolean | Record<string, unknown> | undefined) => {
  spaceStore.setSelectedSpace(typeof spaceId === 'string' ? spaceId : '');
};

const handleCreateSpace = () => {
  router.push('/settings/spaces');
};

onMounted(async () => {
  await spaceStore.loadSpaces();
});
</script>

<style lang="scss" scoped>
.header_crumb {
  display: flex;
  align-items: center;
  width: 100%;
}

.space-selector {
  display: flex;
  align-items: center;
}
</style>
