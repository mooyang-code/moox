<template>
  <div class="tab-panel">
    <a-tabs :active-key="activeTagKey" type="rounded" size="medium" class="tag-tabs" @change="onTagChange">
      <a-tab-pane :key="ALL_TAG_KEY" title="全部" />
      <a-tab-pane v-for="tag in tags" :key="tag.tag_id" :title="tag.tag_name" />
    </a-tabs>
    <div class="filter-bar">
      <div class="filter-query">
        <a-input
          v-model="keyword"
          class="keyword-input"
          allow-clear
          placeholder="搜索对象 ID、名称、类型或市场"
          @press-enter="onSearch"
        />
        <a-select v-model="statusFilter" class="status-select" placeholder="状态">
          <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
        </a-select>
        <a-button type="primary" @click="onSearch">
          <template #icon><icon-search /></template>
          查询
        </a-button>
      </div>
      <a-space class="filter-actions">
        <a-button :loading="loading" @click="reload">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button v-if="selectedTagId && canEditMembers" @click="openBatchAdd">
          <template #icon><icon-plus /></template>
          批量加入
        </a-button>
        <a-button v-if="selectedTagId && canEditMembers" :disabled="!selectedKeys.length" @click="removeSelected">
          移出标签
        </a-button>
        <a-button v-if="selectedTagId && canEditMembers" :disabled="!selectedKeys.length" @click="restoreSelected">
          恢复为有效
        </a-button>
        <a-button v-if="!selectedTagId" type="primary" status="success" @click="openSubjectCreate">
          <template #icon><icon-plus /></template>
          新增对象
        </a-button>
        <a-button v-if="!selectedTagId && selectedKeys.length" :disabled="!manualTags.length" @click="openSelectedAdd">
          加入标签
        </a-button>
      </a-space>
    </div>

    <a-alert v-if="selectedTagId && selectedTag?.mode === 'auto'" type="info" show-icon class="mode-alert">
      成员由数据源自动同步，页面不可直接修改。
    </a-alert>
    <a-alert v-if="selectedTagId && selectedTag?.mode === 'manual' && probeEnabled" type="info" show-icon class="mode-alert">
      手工成员状态会在下一次探测结果中更新。
    </a-alert>

    <a-table
      row-key="subject.subject_id"
      v-model:selected-keys="selectedKeys"
      size="small"
      :bordered="{ cell: true }"
      :loading="loading"
      :data="rows"
      :pagination="pagination"
      :row-selection="{ type: 'checkbox', showCheckedAll: true }"
      :expandable="{ width: 44 }"
      @page-change="onPageChange"
      @page-size-change="onPageSizeChange"
    >
      <template #expand-row="{ record }">
        <div class="attributes-grid">
          <span v-for="[key, value] in subjectAttributes(record)" :key="key">
            <strong>{{ key }}</strong>
            <span>{{ value }}</span>
          </span>
          <span v-if="!subjectAttributes(record).length" class="muted">暂无公共属性</span>
        </div>
      </template>
      <template #columns>
        <a-table-column title="对象 ID" :width="190">
          <template #cell="{ record }">{{ record.subject?.subject_id || "-" }}</template>
        </a-table-column>
        <a-table-column title="名称" :width="180">
          <template #cell="{ record }">{{ record.subject?.name || "-" }}</template>
        </a-table-column>
        <a-table-column title="类型" data-index="subject.subject_type" :width="130" />
        <a-table-column title="市场" data-index="subject.market" :width="110" />
        <a-table-column title="所属标签" :width="220">
          <template #cell="{ record }">
            <a-space wrap>
              <a-tag v-for="tagId in record.tag_ids || (record.tag_id ? [record.tag_id] : [])" :key="tagId" size="small">
                {{ tagName(tagId) }}
              </a-tag>
              <span v-if="!(record.tag_ids || []).length && !record.tag_id" class="muted">—</span>
            </a-space>
          </template>
        </a-table-column>
        <a-table-column v-if="selectedTagId" title="当前标签状态" :width="120">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.status === 'active' ? 'green' : 'gray'">
              {{ record.status === "active" ? "有效" : "失效" }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column v-if="selectedTagId" title="失效时间" :width="180">
          <template #cell="{ record }">{{ formatTime(record.inactive_at) }}</template>
        </a-table-column>
        <a-table-column v-if="!selectedTagId" title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="statusColor(record.subject?.status)">{{ statusLabel(record.subject?.status) }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column v-if="!selectedTagId" title="操作" :width="90" align="center" fixed="right">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openSubjectEdit(record.subject)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-modal v-model:visible="batchVisible" width="560px" title="批量加入标签" @before-ok="submitBatchAdd">
      <a-form auto-label-width>
        <a-form-item label="标签">
          <span>{{ selectedTag?.tag_name }}（{{ selectedTag?.tag_id }}）</span>
        </a-form-item>
        <a-form-item label="对象 ID" required>
          <a-textarea v-model="batchIds" :auto-size="{ minRows: 5, maxRows: 10 }" placeholder="每行一个，也支持逗号分隔" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="selectedAddVisible" width="560px" title="将对象加入标签" @before-ok="submitSelectedAdd">
      <a-form auto-label-width>
        <a-form-item label="标签" required>
          <a-select v-model="selectedAddTagId" placeholder="选择手工标签">
            <a-option v-for="tag in manualTags" :key="tag.tag_id" :value="tag.tag_id">
              {{ tag.tag_name }}（{{ tag.tag_id }}）
            </a-option>
          </a-select>
        </a-form-item>
        <span class="muted">将加入当前页选中的 {{ selectedKeys.length }} 个对象。</span>
      </a-form>
    </a-modal>

    <a-modal
      v-model:visible="subjectVisible"
      width="640px"
      :title="subjectEditing ? '编辑数据对象' : '新增数据对象'"
      @before-ok="submitSubject"
    >
      <a-form :model="subjectForm" auto-label-width>
        <a-form-item field="subject_id" label="对象 ID" required>
          <a-input v-model="subjectForm.subject_id" :disabled="subjectEditing" placeholder="例如 BTC-USDT 或 600000.XSHG" />
        </a-form-item>
        <a-form-item field="subject_type" label="对象类型" required>
          <a-input v-model="subjectForm.subject_type" placeholder="例如 crypto_pair、stock、index" />
        </a-form-item>
        <a-form-item field="name" label="名称" required><a-input v-model="subjectForm.name" /></a-form-item>
        <a-form-item field="market" label="市场"><a-input v-model="subjectForm.market" /></a-form-item>
        <a-form-item field="currency" label="币种"><a-input v-model="subjectForm.currency" /></a-form-item>
        <a-form-item field="timezone" label="时区"><a-input v-model="subjectForm.timezone" /></a-form-item>
        <a-form-item field="status" label="状态">
          <a-select v-model="subjectForm.status">
            <a-option value="active">启用</a-option>
            <a-option value="disabled">停用</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  addTagMembers,
  listTagMembers,
  listTags,
  removeTagMembers,
  setTagMemberStatus,
  upsertSubject
} from "@/api/storage/metadata";
import type { Subject, Tag, TagMember } from "@/api/storage/types";
import { applyPageResult, defaultPagination, formatTime, statusColor, statusLabel } from "@/views/data/shared/metadata-utils";

const props = defineProps<{ spaceId: string; initialTagId?: string }>();

const tags = ref<Tag[]>([]);
const rows = ref<TagMember[]>([]);
const loading = ref(false);
const selectedTagId = ref(props.initialTagId || "");
const statusFilter = ref("");
const keyword = ref("");
const selectedKeys = ref<string[]>([]);
const pagination = reactive(defaultPagination());
const batchVisible = ref(false);
const batchIds = ref("");
const selectedAddVisible = ref(false);
const selectedAddTagId = ref("");
const subjectVisible = ref(false);
const subjectEditing = ref(false);
const subjectForm = reactive<Subject>(emptySubject());

const ALL_TAG_KEY = "__all__";
const activeTagKey = computed(() => selectedTagId.value || ALL_TAG_KEY);
const selectedTag = computed(() => tags.value.find(tag => tag.tag_id === selectedTagId.value));
const manualTags = computed(() => tags.value.filter(tag => tag.mode === "manual"));
const canEditMembers = computed(() => selectedTag.value?.mode === "manual");
const probeEnabled = computed(() => Boolean(selectedTag.value?.mode === "auto" || selectedTag.value?.sources?.length));
const statusOptions = computed(() =>
  selectedTagId.value
    ? [
        { value: "", label: "全部" },
        { value: "active", label: "有效" },
        { value: "inactive", label: "失效" }
      ]
    : [
        { value: "", label: "全部" },
        { value: "active", label: "启用" },
        { value: "disabled", label: "停用" }
      ]
);

function emptySubject(): Subject {
  return {
    space_id: "",
    subject_id: "",
    subject_type: "",
    name: "",
    market: "",
    currency: "",
    timezone: "Asia/Shanghai",
    status: "active"
  };
}

async function loadTags() {
  const rsp = await listTags(props.spaceId);
  tags.value = rsp.tags || [];
  if (selectedTagId.value && !tags.value.some(tag => tag.tag_id === selectedTagId.value)) selectedTagId.value = "";
}

async function reload() {
  if (!props.spaceId) return;
  loading.value = true;
  selectedKeys.value = [];
  try {
    const rsp = await listTagMembers({
      space_id: props.spaceId,
      tag_id: selectedTagId.value || undefined,
      status: statusFilter.value || undefined,
      keyword: keyword.value.trim() || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    rows.value = rsp.members || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

async function load() {
  if (!props.spaceId) return;
  await loadTags();
  await reload();
}

async function onTagChange(key: string | number) {
  const tagId = String(key);
  selectedTagId.value = tagId === ALL_TAG_KEY ? "" : tagId;
  statusFilter.value = "";
  pagination.current = 1;
  await reload();
}

function onSearch() {
  pagination.current = 1;
  void reload();
}

function tagName(tagId: string) {
  return tags.value.find(tag => tag.tag_id === tagId)?.tag_name || tagId;
}

function subjectAttributes(record: TagMember): Array<[string, string]> {
  return Object.entries(record.subject?.attributes || {});
}

function selectedSubjectIds() {
  return selectedKeys.value.filter(Boolean);
}

function openBatchAdd() {
  batchIds.value = "";
  batchVisible.value = true;
}

async function submitBatchAdd(): Promise<boolean> {
  const ids = parseIds(batchIds.value);
  if (!selectedTagId.value || !ids.length) {
    Message.warning("请输入至少一个对象 ID");
    return false;
  }
  await addTagMembers(props.spaceId, selectedTagId.value, ids);
  Message.success(`已加入 ${ids.length} 个对象`);
  batchVisible.value = false;
  await load();
  return true;
}

function parseIds(value: string) {
  return [
    ...new Set(
      value
        .split(/[\n,，]+/)
        .map(item => item.trim())
        .filter(Boolean)
    )
  ];
}

async function removeSelected() {
  const ids = selectedSubjectIds();
  if (!selectedTagId.value || !ids.length) return;
  await removeTagMembers(props.spaceId, selectedTagId.value, ids);
  Message.success("已移出标签");
  await load();
}

async function restoreSelected() {
  const ids = selectedSubjectIds();
  if (!selectedTagId.value || !ids.length) return;
  await setTagMemberStatus(props.spaceId, selectedTagId.value, ids, "active");
  Message.success(probeEnabled.value ? "已恢复为有效，下一次探测结果可能更新状态" : "已恢复为有效");
  await load();
}

function openSelectedAdd() {
  selectedAddTagId.value = manualTags.value[0]?.tag_id || "";
  selectedAddVisible.value = true;
}

async function submitSelectedAdd(): Promise<boolean> {
  const ids = selectedSubjectIds();
  if (!selectedAddTagId.value || !ids.length) {
    Message.warning("请选择标签");
    return false;
  }
  await addTagMembers(props.spaceId, selectedAddTagId.value, ids);
  Message.success("对象已加入标签");
  selectedAddVisible.value = false;
  await load();
  return true;
}

function openSubjectCreate() {
  subjectEditing.value = false;
  Object.assign(subjectForm, emptySubject(), { space_id: props.spaceId });
  subjectVisible.value = true;
}

function openSubjectEdit(subject: Subject) {
  subjectEditing.value = true;
  Object.assign(subjectForm, subject, { timezone: subject.timezone || "Asia/Shanghai" });
  subjectVisible.value = true;
}

async function submitSubject(): Promise<boolean> {
  if (!subjectForm.subject_id || !subjectForm.subject_type || !subjectForm.name) {
    Message.warning("请补全对象 ID、对象类型和名称");
    return false;
  }
  await upsertSubject({ ...subjectForm, space_id: props.spaceId });
  Message.success("数据对象已保存");
  subjectVisible.value = false;
  await reload();
  return true;
}

function onPageChange(page: number) {
  pagination.current = page;
  void reload();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  void reload();
}

watch(
  () => props.initialTagId,
  value => {
    if (value !== undefined) {
      selectedTagId.value = value;
      statusFilter.value = "";
      pagination.current = 1;
      void reload();
    }
  }
);

watch(
  () => props.spaceId,
  () => {
    selectedTagId.value = props.initialTagId || "";
    statusFilter.value = "";
    pagination.current = 1;
    void load();
  },
  { immediate: true }
);
</script>

<style scoped>
.tab-panel {
  min-width: 0;
}

.filter-bar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}

.filter-query {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: var(--moox-space-2);
}

.tag-tabs {
  min-width: 0;
  margin-bottom: var(--moox-space-3);
}

.tag-tabs :deep(.arco-tabs-content) {
  display: none;
}

.status-select {
  width: 120px;
  flex: 0 0 120px;
}

.keyword-input {
  width: 280px;
  flex: 0 0 280px;
}

.filter-actions {
  margin-left: auto;
}

.mode-alert {
  margin-bottom: var(--moox-space-3);
}

.attributes-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px 24px;
  padding: 4px 36px 8px;
  color: var(--color-text-2);
}

.attributes-grid span {
  display: flex;
  gap: 8px;
}

.attributes-grid strong {
  color: var(--color-text-1);
}

.muted {
  color: var(--color-text-3);
}
</style>
