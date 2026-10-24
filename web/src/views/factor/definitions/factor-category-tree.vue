<template>
  <div class="category-tree">
    <div class="category-tree-head">
      <strong>因子分类</strong>
      <a-tooltip content="新建分类">
        <a-button size="mini" type="text" aria-label="新建分类" @click="openCreate"><icon-plus /></a-button>
      </a-tooltip>
    </div>
    <button class="tree-item" :class="{ active: !props.selected }" type="button" @click="emit('select', '')">
      <icon-apps /><span class="tree-label">全部因子</span><span class="tree-count">{{ props.total }}</span>
    </button>
    <div v-for="category in props.categories" :key="category.category_id" class="tree-row">
      <button
        class="tree-item"
        :class="{ active: props.selected === category.category_id }"
        type="button"
        @click="emit('select', category.category_id)"
      >
        <icon-folder /><span class="tree-label">{{ category.name }}</span
        ><span class="tree-count">{{ props.counts[category.category_id] || 0 }}</span>
      </button>
      <a-dropdown v-if="category.category_id !== UNCATEGORIZED" trigger="click">
        <a-button class="node-menu" size="mini" type="text" aria-label="分类操作"><icon-more /></a-button>
        <template #content>
          <a-doption @click="openRename(category)">重命名</a-doption>
          <a-doption class="danger-option" @click="emit('delete', category)">删除</a-doption>
        </template>
      </a-dropdown>
      <span v-else />
    </div>

    <a-modal
      :visible="formVisible"
      :title="editing ? '重命名分类' : '新建分类'"
      :ok-button-props="{ disabled: !canSave }"
      :ok-loading="props.saving"
      unmount-on-close
      @ok="submit"
      @cancel="formVisible = false"
    >
      <a-form :model="form" layout="vertical" @submit.prevent="submit">
        <a-form-item label="分类 ID" :help="editing ? '分类 ID 创建后不可修改' : '小写字母开头，只含小写字母、数字、下划线'">
          <a-input v-model="form.category_id" :disabled="Boolean(editing)" placeholder="momentum" :max-length="32" />
        </a-form-item>
        <a-form-item label="名称">
          <a-input v-model="form.name" placeholder="动量类" :max-length="32" @press-enter="submit" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { UNCATEGORIZED, type FactorCategory } from "@/api/factor/types";
import { CATEGORY_ID_PATTERN } from "./definitions-model";

const props = defineProps<{
  /** 已按 orderedCategories 排好，未分类在最后。 */
  categories: FactorCategory[];
  counts: Record<string, number>;
  total: number;
  /** 选中的分类 ID，空为全部。 */
  selected: string;
  saving?: boolean;
}>();

const emit = defineEmits<{
  (event: "select", categoryId: string): void;
  // done 由父组件在接口成功后调用，关闭表单。
  (event: "create", category: { category_id: string; name: string }, done: () => void): void;
  (event: "rename", category: FactorCategory, name: string, done: () => void): void;
  (event: "delete", category: FactorCategory): void;
}>();

const formVisible = ref(false);
const editing = ref<FactorCategory>();
const form = reactive({ category_id: "", name: "" });

const canSave = computed(() => {
  const name = form.name.trim();
  if (!name) return false;
  if (editing.value) return name !== editing.value.name;
  return CATEGORY_ID_PATTERN.test(form.category_id.trim());
});

function openCreate() {
  editing.value = undefined;
  form.category_id = "";
  form.name = "";
  formVisible.value = true;
}

function openRename(category: FactorCategory) {
  editing.value = category;
  form.category_id = category.category_id;
  form.name = category.name;
  formVisible.value = true;
}

/** 由父组件调用接口；成功后父组件调用 done 关闭表单。 */
function submit() {
  if (!canSave.value) return;
  const done = () => {
    formVisible.value = false;
  };
  if (editing.value) emit("rename", editing.value, form.name.trim(), done);
  else emit("create", { category_id: form.category_id.trim(), name: form.name.trim() }, done);
}
</script>

<style scoped>
.category-tree {
  padding: 10px var(--moox-space-2);
}
.category-tree-head {
  display: flex;
  height: 34px;
  padding: 0 6px 6px 10px;
  align-items: center;
  justify-content: space-between;
}
.tree-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 28px;
  align-items: center;
}
.tree-row:hover .node-menu {
  opacity: 1;
}
.tree-item {
  display: flex;
  width: 100%;
  min-width: 0;
  height: 34px;
  padding: 0 var(--moox-space-2);
  border: 0;
  border-radius: 4px;
  align-items: center;
  gap: var(--moox-space-2);
  background: transparent;
  color: var(--color-text-2);
  cursor: pointer;
  text-align: left;
}
.tree-item:hover {
  background: var(--color-fill-2);
}
.tree-item.active {
  background: rgb(var(--primary-1));
  color: rgb(var(--primary-6));
  font-weight: 600;
}
.tree-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tree-count {
  margin-left: auto;
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: 400;
}
.node-menu {
  opacity: 0;
}
:deep(.danger-option) {
  color: rgb(var(--danger-6));
}
</style>
