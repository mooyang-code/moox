<template>
  <div class="moox-page fields-page">
    <div class="moox-inner fields-inner">
      <div class="toolbar">
        <div class="toolbar-main">
          <h2 class="page-title">字段管理</h2>
          <a-button class="mobile-group-trigger" @click="mobileGroupVisible = true"><template #icon><icon-menu /></template>字段组</a-button>
          <div class="keyword-control">
            <a-input-search v-model="searchKeyword" class="keyword-input" allow-clear placeholder="搜索字段 ID、中文名或描述" @input="scheduleSearch" @search="commitSearch" />
          </div>
          <div class="filter-control">
            <a-select v-model="state.valueType" class="filter-select" allow-clear placeholder="值类型" @change="changeFilter">
              <a-option v-for="item in fieldValueTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
          </div>
          <div class="filter-control">
            <a-select v-model="state.status" class="filter-select" allow-clear placeholder="状态" @change="changeFilter">
              <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
          </div>
          <a-button type="primary" :disabled="!selectedSpaceId" @click="commitSearch">
            <template #icon><icon-search /></template>查询
          </a-button>
        </div>
        <div class="toolbar-actions">
          <a-button type="primary" status="success" :disabled="!selectedSpaceId || !groups.length" @click="openCreateField">
            <template #icon><icon-plus /></template>新建字段
          </a-button>
        </div>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <section class="field-workbench">
          <aside class="group-panel">
            <a-alert v-if="groupError" type="error" class="inline-error">字段组加载失败 <a-link @click="loadGroups">重试</a-link></a-alert>
            <FieldGroupTree
              :groups="groups"
              :counts="fieldCounts"
              :total="totalFieldCount"
              :ungrouped="ungroupedFieldCount"
              :selected="state.group"
              :loading="groupsLoading"
              @select="selectGroup"
              @create="openCreateGroup"
              @edit="openEditGroup"
              @delete="confirmDeleteGroup"
            />
          </aside>

          <main class="field-main">
            <FieldBatchBar :selected-count="selectedKeys.length" :groups="groups" :loading="batchLoading" @apply="applyBatch" @clear="selectedKeys = []" />
            <a-alert v-if="fieldError" type="error" class="table-error">字段列表加载失败 <a-link @click="loadFields">重试</a-link></a-alert>
            <FieldTable
              :rows="rows"
              :groups="groups"
              :loading="loading"
              :pagination="pagination"
              :selected-keys="selectedKeys"
              :empty-text="emptyText"
              @select="selectedKeys = $event"
              @edit="openEditField"
              @page-change="changePage"
              @page-size-change="changePageSize"
              @sort="changeSort"
            />
          </main>
        </section>
      </template>

      <a-drawer v-model:visible="mobileGroupVisible" title="字段组" width="280px" :footer="false">
        <FieldGroupTree
          :groups="groups"
          :counts="fieldCounts"
          :total="totalFieldCount"
          :ungrouped="ungroupedFieldCount"
          :selected="state.group"
          :loading="groupsLoading"
          @select="selectGroupFromMobile"
          @create="openCreateGroup"
          @edit="openEditGroup"
          @delete="confirmDeleteGroup"
        />
      </a-drawer>

      <FieldEditorDrawer
        :visible="fieldDrawerVisible"
        :field="editingField"
        :groups="groups"
        :initial-group-i-d="initialFieldGroupID"
        :space-i-d="selectedSpaceId"
        :saving="fieldSaving"
        @close="closeFieldDrawer"
        @dirty-change="fieldDrawerDirty = $event"
        @save="saveField"
      />

      <FieldGroupDialog
        :visible="groupDialogVisible"
        :group="editingGroup"
        :parent-i-d="newGroupParentID"
        :groups="groups"
        :space-i-d="selectedSpaceId"
        :saving="groupSaving"
        @close="groupDialogVisible = false"
        @save="saveGroup"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue';
import { Message, Modal } from '@arco-design/web-vue';
import { useRoute, useRouter } from 'vue-router';
import {
  batchUpdateFields,
  createField,
  createFieldGroup,
  deleteFieldGroup,
  listFieldGroups,
  listFields,
  updateField,
  updateFieldGroup,
} from '@/api/storage/metadata';
import type { Field, FieldGroup } from '@/api/storage/types';
import { useSpaceStore } from '@/store/modules/space';
import { applyPageResult, defaultPagination, fieldValueTypeOptions, statusOptions } from '@/views/data/shared/metadata-utils';
import FieldBatchBar from './components/FieldBatchBar.vue';
import FieldEditorDrawer from './components/FieldEditorDrawer.vue';
import FieldGroupDialog from './components/FieldGroupDialog.vue';
import FieldGroupTree from './components/FieldGroupTree.vue';
import FieldTable from './components/FieldTable.vue';
import { fieldQueryFromRoute, fieldQueryToRoute, RequestGate } from './field-workbench';

defineOptions({ name: 'DataFields' });
const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const state = reactive(fieldQueryFromRoute(route.query));
const searchKeyword = ref(state.keyword);
const rows = ref<Field[]>([]);
const groups = ref<FieldGroup[]>([]);
const fieldCounts = ref<Record<string, number>>({});
const totalFieldCount = ref(0);
const ungroupedFieldCount = ref(0);
const selectedKeys = ref<string[]>([]);
const loading = ref(false);
const groupsLoading = ref(false);
const fieldError = ref(false);
const groupError = ref(false);
const batchLoading = ref(false);
const fieldSaving = ref(false);
const groupSaving = ref(false);
const fieldDrawerVisible = ref(false);
const fieldDrawerDirty = ref(false);
const groupDialogVisible = ref(false);
const mobileGroupVisible = ref(false);
const editingField = ref<Field | null>(null);
const editingGroup = ref<FieldGroup | null>(null);
const newGroupParentID = ref('');
const routeReady = ref(false);
const replacingRoute = ref(false);
const pagination = reactive({ ...defaultPagination(), pageSizeOptions: [20, 50, 100], showPageSize: true, showTotal: true });
const fieldGate = new RequestGate();
const groupGate = new RequestGate();
let searchTimer: ReturnType<typeof setTimeout> | undefined;
let revertingSpace = false;

const initialFieldGroupID = computed(() => {
  if (state.group && state.group !== 'ungrouped' && groups.value.some((item) => item.group_id === state.group)) return state.group;
  return groups.value.find((item) => item.parent_group_id)?.group_id || groups.value[0]?.group_id || '';
});
const emptyText = computed(() => state.keyword || state.valueType || state.status ? '没有符合条件的字段' : '当前范围暂无字段');

async function replaceQuery() {
  replacingRoute.value = true;
  try {
    await router.replace({ query: fieldQueryToRoute(state) });
  } finally {
    replacingRoute.value = false;
  }
}

async function loadGroups() {
  if (!selectedSpaceId.value) return;
  const token = groupGate.next();
  groupsLoading.value = true;
  groupError.value = false;
  try {
    const all: FieldGroup[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFieldGroups({ space_id: selectedSpaceId.value, page: { page, size: 200 } });
      if (!groupGate.isCurrent(token)) return;
      all.push(...(rsp.field_groups || []));
      if (page === 1) {
        fieldCounts.value = rsp.field_counts || {};
        totalFieldCount.value = Number(rsp.total_field_count || 0);
        ungroupedFieldCount.value = Number(rsp.ungrouped_field_count || 0);
      }
      if (!rsp.page_result?.has_more || !(rsp.field_groups || []).length) break;
    }
    groups.value = all;
    if (state.group && state.group !== 'ungrouped' && !all.some((item) => item.group_id === state.group)) {
      state.group = '';
      state.page = 1;
      await replaceQuery();
    }
  } catch {
    if (groupGate.isCurrent(token)) groupError.value = true;
  } finally {
    if (groupGate.isCurrent(token)) groupsLoading.value = false;
  }
}

async function loadFields() {
  if (!selectedSpaceId.value) return;
  const token = fieldGate.next();
  loading.value = true;
  fieldError.value = false;
  try {
    const selected = groups.value.find((item) => item.group_id === state.group);
    const rsp = await listFields({
      space_id: selectedSpaceId.value,
      group_id: state.group && state.group !== 'ungrouped' ? state.group : undefined,
      include_descendants: Boolean(selected && !selected.parent_group_id),
      ungrouped_only: state.group === 'ungrouped' || undefined,
      value_type: state.valueType || undefined,
      status: state.status || undefined,
      keyword: state.keyword || undefined,
      sort_by: state.sort,
      sort_order: state.order,
      page: { page: state.page, size: state.pageSize },
    });
    if (!fieldGate.isCurrent(token)) return;
    rows.value = rsp.fields || [];
    pagination.current = state.page;
    pagination.pageSize = state.pageSize;
    applyPageResult(pagination, rsp.page_result);
  } catch {
    if (fieldGate.isCurrent(token)) fieldError.value = true;
  } finally {
    if (fieldGate.isCurrent(token)) loading.value = false;
  }
}

async function loadAll() {
  await loadGroups();
  await loadFields();
}

async function commitState(patch: Partial<typeof state>) {
  Object.assign(state, patch);
  selectedKeys.value = [];
  await replaceQuery();
  await loadFields();
}

function scheduleSearch() {
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(commitSearch, 300);
}

function commitSearch() {
  if (searchTimer) clearTimeout(searchTimer);
  void commitState({ keyword: searchKeyword.value.trim(), page: 1 });
}

function changeFilter() { void commitState({ page: 1 }); }
function selectGroup(groupID: string) { void commitState({ group: groupID, page: 1 }); }
function selectGroupFromMobile(groupID: string) { mobileGroupVisible.value = false; selectGroup(groupID); }
function changePage(page: number) { void commitState({ page }); }
function changePageSize(pageSize: number) { void commitState({ page: 1, pageSize }); }
function changeSort(sort: 'sort_order' | 'field_id' | 'updated_at', order: 'asc' | 'desc') { void commitState({ sort, order, page: 1 }); }

function openCreateField() { editingField.value = null; fieldDrawerVisible.value = true; }
function openEditField(field: Field) { editingField.value = field; fieldDrawerVisible.value = true; }
function closeFieldDrawer() { fieldDrawerVisible.value = false; editingField.value = null; }

async function resetForSpace() {
  fieldGate.next();
  groupGate.next();
  closeFieldDrawer();
  groupDialogVisible.value = false;
  groups.value = [];
  rows.value = [];
  selectedKeys.value = [];
  Object.assign(state, fieldQueryFromRoute({}));
  searchKeyword.value = '';
  await replaceQuery();
  await loadAll();
}

async function saveField(field: Field) {
  fieldSaving.value = true;
  try {
    if (editingField.value) await updateField(field); else await createField(field);
    Message.success('字段已保存');
    closeFieldDrawer();
    selectedKeys.value = [];
    await loadAll();
  } finally {
    fieldSaving.value = false;
  }
}

function openCreateGroup(parentID = '') {
  editingGroup.value = null;
  newGroupParentID.value = parentID;
  groupDialogVisible.value = true;
  mobileGroupVisible.value = false;
}
function openEditGroup(group: FieldGroup) {
  editingGroup.value = group;
  newGroupParentID.value = '';
  groupDialogVisible.value = true;
  mobileGroupVisible.value = false;
}

async function saveGroup(group: FieldGroup) {
  groupSaving.value = true;
  try {
    if (editingGroup.value) await updateFieldGroup(group); else await createFieldGroup(group);
    Message.success('字段组已保存');
    groupDialogVisible.value = false;
    await loadGroups();
  } finally {
    groupSaving.value = false;
  }
}

function confirmDeleteGroup(group: FieldGroup) {
  mobileGroupVisible.value = false;
  Modal.confirm({
    title: `删除字段组“${group.name}”？`,
    content: '只有不包含子组和字段的空字段组可以删除。',
    okText: '删除',
    okButtonProps: { status: 'danger' },
    onOk: async () => {
      await deleteFieldGroup({ space_id: selectedSpaceId.value, group_id: group.group_id });
      Message.success('字段组已删除');
      if (state.group === group.group_id) {
        state.group = '';
        state.page = 1;
        await replaceQuery();
      }
      await loadAll();
    },
  });
}

async function applyBatch(change: { target_group_id?: string; target_status?: 'active' | 'disabled' }) {
  if (!selectedKeys.value.length) return;
  batchLoading.value = true;
  try {
    const rsp = await batchUpdateFields({ space_id: selectedSpaceId.value, field_ids: selectedKeys.value, ...change });
    Message.success(`已更新 ${rsp.updated_count || selectedKeys.value.length} 个字段`);
    selectedKeys.value = [];
    await loadAll();
  } finally {
    batchLoading.value = false;
  }
}

watch(() => route.query, async (query) => {
  if (!routeReady.value || replacingRoute.value) return;
  Object.assign(state, fieldQueryFromRoute(query));
  searchKeyword.value = state.keyword;
  selectedKeys.value = [];
  await loadFields();
});

watch(selectedSpaceId, async (nextSpaceID, previousSpaceID) => {
  if (revertingSpace) {
    revertingSpace = false;
    return;
  }
  if (fieldDrawerVisible.value && fieldDrawerDirty.value && previousSpaceID) {
    revertingSpace = true;
    spaceStore.setSelectedSpace(previousSpaceID);
    Modal.confirm({
      title: '切换空间并放弃修改？',
      content: '当前字段存在未保存修改。',
      okText: '放弃修改',
      onOk: () => {
        closeFieldDrawer();
        spaceStore.setSelectedSpace(nextSpaceID);
      },
    });
    return;
  }
  await resetForSpace();
});

onMounted(async () => {
  await loadAll();
  routeReady.value = true;
});
</script>

<style scoped>
.fields-inner { display: flex; min-height: calc(100vh - 116px); flex-direction: column; }
.toolbar { display: flex; min-height: 48px; margin-bottom: 8px; align-items: center; justify-content: space-between; gap: 16px; }
.toolbar-main { display: flex; min-width: 0; flex: 1 1 auto; align-items: center; gap: 8px; }
.page-title { flex: 0 0 auto; margin: 0 8px 0 0; font-size: 20px; font-weight: 600; }
.toolbar-actions { display: flex; flex: 0 0 auto; align-items: center; gap: 8px; white-space: nowrap; }
.keyword-control { min-width: 220px; flex: 1 1 360px; }
.filter-control { width: 140px; flex: 0 0 140px; }
.keyword-input, .filter-select { width: 100%; }
.mobile-group-trigger { display: none; }
.field-workbench { display: grid; min-height: 560px; flex: 1; grid-template-columns: 240px minmax(0, 1fr); border: 1px solid var(--color-border-2); background: var(--color-bg-2); }
.group-panel { min-height: 0; overflow: auto; border-right: 1px solid var(--color-border-2); background: var(--color-fill-1); }
.field-main { min-width: 0; overflow: hidden; }
.inline-error, .table-error { margin: 8px; }
.table-error { margin-bottom: 0; }
@media (max-width: 900px) {
  .group-panel { display: none; }
  .field-workbench { grid-template-columns: minmax(0, 1fr); }
  .mobile-group-trigger { display: inline-flex; }
  .toolbar { flex-wrap: wrap; gap: 8px; }
  .toolbar-main { flex: 1 1 100%; flex-wrap: wrap; }
  .toolbar-actions { margin-left: auto; }
  .keyword-control { min-width: 220px; flex: 1 1 260px; }
  .filter-control { width: auto; flex: 1 1 130px; }
}
@media (max-width: 560px) {
  .toolbar-main, .toolbar-actions { width: 100%; }
  .toolbar-actions { justify-content: flex-end; }
  .field-workbench { min-height: 480px; }
}
</style>
