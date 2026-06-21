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
            <a-button type="text" size="small" @click="openCreate" style="margin-top: 8px;">
              新建空间
            </a-button>
          </div>
        </template>
      </a-select>
      <a-button type="text" size="small" title="新建空间" style="margin-left: 8px;" @click="openCreate">
        <template #icon><icon-plus /></template>
      </a-button>
    </div>

    <a-modal
      v-model:visible="createVisible"
      title="新建空间"
      :on-before-ok="submitCreate"
      @cancel="resetCreate"
    >
      <a-form :model="createForm" layout="vertical">
        <a-form-item label="空间 ID" required>
          <a-input v-model="createForm.space_id" placeholder="如 hk_stock" />
        </a-form-item>
        <a-form-item label="名称" required>
          <a-input v-model="createForm.name" placeholder="空间名称" />
        </a-form-item>
        <a-form-item label="描述">
          <a-input v-model="createForm.description" />
        </a-form-item>
        <a-form-item label="负责人">
          <a-input v-model="createForm.owner" />
        </a-form-item>
        <a-form-item label="市场">
          <a-input v-model="createForm.market" placeholder="如 HK / US / CN" />
        </a-form-item>
        <a-form-item label="时区">
          <a-input v-model="createForm.timezone" placeholder="如 Asia/Shanghai" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { onMounted, computed, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import ButtonCollapsed from "@/layout/components/Header/components/button-collapsed/index.vue";
import { useSpaceStore } from '@/store/modules/space';
import { createSpace } from '@/api/control/spaces';
import { storeToRefs } from 'pinia';
import type { Space } from '@/api/control/types';

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

const createVisible = ref(false);
const createForm = reactive({
  space_id: "",
  name: "",
  description: "",
  owner: "",
  market: "",
  timezone: ""
});

const resetCreate = () => {
  createForm.space_id = "";
  createForm.name = "";
  createForm.description = "";
  createForm.owner = "";
  createForm.market = "";
  createForm.timezone = "";
};

const openCreate = () => {
  resetCreate();
  createVisible.value = true;
};

const submitCreate = async (): Promise<boolean> => {
  const spaceId = createForm.space_id.trim();
  const name = createForm.name.trim();
  if (!spaceId || !name) {
    Message.warning("请填写空间 ID 和名称");
    return false;
  }
  try {
    await createSpace({ ...createForm, space_id: spaceId, name, status: "active" });
    await spaceStore.loadSpaces();
    spaceStore.setSelectedSpace(spaceId);
    Message.success("空间创建成功");
    resetCreate();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "创建空间失败");
    return false;
  }
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
