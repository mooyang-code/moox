<template>
  <a-modal
    :visible="props.visible"
    title="管理因子分类"
    :footer="false"
    unmount-on-close
    :width="560"
    @cancel="emit('update:visible', false)"
  >
    <p class="category-hint">
      分类只用于归类与筛选（如动量类、波动类），不影响计算。「未分类」是内置分类，不能删除；仍被因子使用的分类不能删除。
    </p>
    <a-table :data="props.categories" size="small" row-key="category_id" :pagination="false" :bordered="{ cell: true }">
      <template #columns>
        <a-table-column title="分类 ID" data-index="category_id" :width="140" />
        <a-table-column title="名称" data-index="name" />
        <a-table-column title="操作" :width="90" align="center">
          <template #cell="{ record }">
            <a-popconfirm content="确认删除这个分类？" @ok="remove(record.category_id)">
              <a-button size="mini" type="text" status="danger" :disabled="record.category_id === UNCATEGORIZED">删除</a-button>
            </a-popconfirm>
          </template>
        </a-table-column>
      </template>
    </a-table>
    <a-form class="category-form" layout="inline" :model="form" @submit.prevent="save">
      <a-form-item field="category_id" label="分类 ID">
        <a-input v-model="form.category_id" placeholder="momentum" :max-length="32" style="width: 150px" />
      </a-form-item>
      <a-form-item field="name" label="名称">
        <a-input v-model="form.name" placeholder="动量类" :max-length="64" style="width: 150px" />
      </a-form-item>
      <a-button type="primary" status="success" html-type="submit" :loading="saving" :disabled="!canSave">新增 / 改名</a-button>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { deleteFactorCategory, upsertFactorCategory } from "@/api/factor";
import type { FactorCategory } from "@/api/factor/types";
import { UNCATEGORIZED } from "@/api/factor/types";

defineOptions({ name: "FactorCategoryManager" });

const props = defineProps<{ visible: boolean; categories: FactorCategory[] }>();
const emit = defineEmits<{ (e: "update:visible", value: boolean): void; (e: "changed"): void }>();

const CATEGORY_ID = /^[a-z][a-z0-9_]{0,31}$/;
const form = reactive({ category_id: "", name: "" });
const saving = ref(false);

const canSave = computed(() => CATEGORY_ID.test(form.category_id.trim()) && form.name.trim().length > 0);

async function save() {
  if (!canSave.value) return;
  saving.value = true;
  try {
    await upsertFactorCategory({ category_id: form.category_id.trim(), name: form.name.trim() });
    Message.success("分类已保存");
    form.category_id = "";
    form.name = "";
    emit("changed");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "保存分类失败");
  } finally {
    saving.value = false;
  }
}

async function remove(categoryId: string) {
  try {
    await deleteFactorCategory(categoryId);
    Message.success("分类已删除");
    emit("changed");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除分类失败");
  }
}
</script>

<style scoped>
.category-hint {
  margin: 0 0 12px;
  color: var(--color-text-3);
  font-size: 12px;
}

.category-form {
  margin-top: 16px;
}
</style>
