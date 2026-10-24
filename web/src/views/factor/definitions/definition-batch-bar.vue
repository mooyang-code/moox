<template>
  <div v-if="selectedCount" class="batch-bar">
    <span
      >已选择 <strong>{{ selectedCount }}</strong> 个因子</span
    >
    <a-divider direction="vertical" />
    <a-button size="small" :disabled="loading" @click="moveVisible = true"
      ><template #icon><icon-folder /></template>移动分类</a-button
    >
    <a-button size="small" type="text" :disabled="loading" @click="$emit('clear')">取消选择</a-button>
  </div>

  <a-modal v-model:visible="moveVisible" title="移动分类" width="480px" :ok-loading="loading" @ok="submitMove">
    <a-form-item label="目标分类" required>
      <a-select v-model="targetCategoryID" placeholder="请选择目标分类">
        <a-option v-for="category in categories" :key="category.category_id" :value="category.category_id">{{
          category.name
        }}</a-option>
      </a-select>
    </a-form-item>
  </a-modal>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Message } from "@arco-design/web-vue";
import type { FactorCategory } from "@/api/factor/types";

defineProps<{ selectedCount: number; categories: FactorCategory[]; loading: boolean }>();
const emit = defineEmits<{ move: [categoryId: string]; clear: [] }>();

const moveVisible = ref(false);
const targetCategoryID = ref("");

function submitMove() {
  if (!targetCategoryID.value) {
    Message.warning("请选择目标分类");
    return false;
  }
  emit("move", targetCategoryID.value);
  moveVisible.value = false;
  targetCategoryID.value = "";
  return true;
}
</script>

<style scoped>
.batch-bar {
  display: flex;
  min-height: 44px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border-2);
  align-items: center;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  background: rgb(var(--primary-1));
}
.batch-bar > span {
  color: var(--color-text-2);
}
</style>
