<template>
  <div v-if="selectedCount" class="batch-bar">
    <span>已选择 <strong>{{ selectedCount }}</strong> 个字段</span>
    <a-divider direction="vertical" />
    <a-button size="small" :disabled="loading" @click="moveVisible = true"><template #icon><icon-folder /></template>移动字段组</a-button>
    <a-button size="small" :loading="loading" @click="$emit('apply', { target_status: 'active' })"><template #icon><icon-check-circle /></template>启用</a-button>
    <a-popconfirm content="确认停用所选字段？" @ok="$emit('apply', { target_status: 'disabled' })">
      <a-button size="small" status="warning" :disabled="loading"><template #icon><icon-minus-circle /></template>停用</a-button>
    </a-popconfirm>
    <a-button size="small" type="text" :disabled="loading" @click="$emit('clear')">取消选择</a-button>
  </div>

  <a-modal v-model:visible="moveVisible" title="移动字段组" width="480px" :ok-loading="loading" @ok="submitMove">
    <a-form-item label="目标字段组" required>
      <a-select v-model="targetGroupID" placeholder="请选择目标字段组">
        <a-option v-for="group in groups" :key="group.group_id" :value="group.group_id">{{ groupPath(groups, group.group_id) }}</a-option>
      </a-select>
    </a-form-item>
  </a-modal>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import type { FieldGroup } from '@/api/storage/types';
import { groupPath } from '../field-workbench';

defineProps<{ selectedCount: number; groups: FieldGroup[]; loading: boolean }>();
const emit = defineEmits<{
  apply: [change: { target_group_id?: string; target_status?: 'active' | 'disabled' }];
  clear: [];
}>();

const moveVisible = ref(false);
const targetGroupID = ref('');

function submitMove() {
  if (!targetGroupID.value) {
    Message.warning('请选择目标字段组');
    return false;
  }
  emit('apply', { target_group_id: targetGroupID.value });
  moveVisible.value = false;
  targetGroupID.value = '';
  return true;
}
</script>

<style scoped>
.batch-bar { display: flex; min-height: 44px; padding: 6px 10px; border-bottom: 1px solid var(--color-border-2); align-items: center; flex-wrap: wrap; gap: 8px; background: rgb(var(--primary-1)); }
.batch-bar > span { color: var(--color-text-2); }
</style>
