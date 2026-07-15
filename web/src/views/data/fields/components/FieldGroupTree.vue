<template>
  <div class="group-tree">
    <div class="group-tree-head">
      <strong>字段组</strong>
      <a-tooltip content="新建一级字段组">
        <a-button size="mini" type="text" @click="$emit('create', '')"><icon-plus /></a-button>
      </a-tooltip>
    </div>
    <a-spin :loading="loading" class="group-tree-spin">
      <button class="tree-item" :class="{ active: selected === '' }" type="button" @click="$emit('select', '')">
        <icon-apps /><span class="tree-label">全部字段</span><span class="tree-count">{{ total }}</span>
      </button>
      <button
        v-if="ungrouped > 0"
        class="tree-item"
        :class="{ active: selected === 'ungrouped' }"
        type="button"
        @click="$emit('select', 'ungrouped')"
      >
        <icon-exclamation-circle /><span class="tree-label">未分组字段</span><span class="tree-count">{{ ungrouped }}</span>
      </button>

      <div v-for="root in tree" :key="root.group_id" class="tree-group">
        <div class="tree-row">
          <button class="expand-button" type="button" :aria-label="expanded.has(root.group_id) ? '折叠' : '展开'" @click="toggle(root.group_id)">
            <icon-down v-if="expanded.has(root.group_id)" /><icon-right v-else />
          </button>
          <button class="tree-item" :class="{ active: selected === root.group_id }" type="button" @click="$emit('select', root.group_id)">
            <icon-folder /><span class="tree-label">{{ root.name }}</span><span class="tree-count">{{ counts[root.group_id] || 0 }}</span>
          </button>
          <a-dropdown trigger="click">
            <a-button class="node-menu" size="mini" type="text"><icon-more /></a-button>
            <template #content>
              <a-doption @click="$emit('create', root.group_id)">新增子组</a-doption>
              <a-doption @click="$emit('edit', root)">编辑</a-doption>
              <a-doption class="danger-option" @click="$emit('delete', root)">删除</a-doption>
            </template>
          </a-dropdown>
        </div>
        <div v-if="expanded.has(root.group_id)">
          <div v-for="child in root.children" :key="child.group_id" class="tree-row child-row">
            <span class="child-guide" />
            <button class="tree-item" :class="{ active: selected === child.group_id }" type="button" @click="$emit('select', child.group_id)">
              <icon-file /><span class="tree-label">{{ child.name }}</span><span class="tree-count">{{ counts[child.group_id] || 0 }}</span>
            </button>
            <a-dropdown trigger="click">
              <a-button class="node-menu" size="mini" type="text"><icon-more /></a-button>
              <template #content>
                <a-doption @click="$emit('edit', child)">编辑</a-doption>
                <a-doption class="danger-option" @click="$emit('delete', child)">删除</a-doption>
              </template>
            </a-dropdown>
          </div>
        </div>
      </div>
      <a-empty v-if="!loading && !tree.length" description="暂无字段组" />
    </a-spin>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue';
import type { FieldGroup } from '@/api/storage/types';
import { buildGroupTree } from '../field-workbench';

const props = defineProps<{
  groups: FieldGroup[];
  counts: Record<string, number>;
  total: number;
  ungrouped: number;
  selected: string;
  loading: boolean;
}>();

defineEmits<{
  select: [groupID: string];
  create: [parentID: string];
  edit: [group: FieldGroup];
  delete: [group: FieldGroup];
}>();

const tree = computed(() => buildGroupTree(props.groups));
const expanded = reactive(new Set<string>());

watch(tree, (items) => {
  items.forEach((item) => expanded.add(item.group_id));
}, { immediate: true });

function toggle(groupID: string) {
  if (expanded.has(groupID)) expanded.delete(groupID);
  else expanded.add(groupID);
}
</script>

<style scoped>
.group-tree { height: 100%; min-height: 0; padding: 10px 8px; }
.group-tree-head { display: flex; height: 34px; padding: 0 6px 6px 10px; align-items: center; justify-content: space-between; }
.group-tree-spin { display: block; min-height: 160px; }
.tree-group { margin-top: 2px; }
.tree-row { display: grid; grid-template-columns: 24px minmax(0, 1fr) 28px; align-items: center; }
.tree-row:hover .node-menu { opacity: 1; }
.child-row { padding-left: 18px; }
.tree-item { display: flex; min-width: 0; height: 34px; padding: 0 8px; border: 0; border-radius: 4px; align-items: center; gap: 8px; background: transparent; color: var(--color-text-2); cursor: pointer; text-align: left; }
.group-tree-spin > .tree-item { width: 100%; }
.tree-item:hover { background: var(--color-fill-2); }
.tree-item.active { background: rgb(var(--primary-1)); color: rgb(var(--primary-6)); font-weight: 600; }
.tree-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tree-count { margin-left: auto; color: var(--color-text-3); font-size: 12px; font-weight: 400; }
.expand-button { display: grid; width: 24px; height: 30px; padding: 0; border: 0; place-items: center; background: transparent; color: var(--color-text-3); cursor: pointer; }
.child-guide { width: 24px; }
.node-menu { opacity: 0; }
:deep(.danger-option) { color: rgb(var(--danger-6)); }
</style>
