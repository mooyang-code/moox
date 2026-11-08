<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>数据源</h2>
        <a-space>
          <a-input-search
            v-model="searchKeyword"
            allow-clear
            placeholder="搜索来源ID或名称"
            style="width: 280px"
            @search="onSearch"
            @clear="onSearch"
          />
          <a-button type="primary" status="success" :disabled="!selectedSpaceId" @click="openCreate">
            <template #icon><icon-plus /></template>
            新建来源
          </a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>

      <a-table
        v-else
        row-key="data_source_id"
        size="small"
        :bordered="{ cell: true }"
        :loading="loading"
        :data="rows"
        :pagination="pagination"
        :scroll="{ x: 'max-content' }"
        @page-change="onPageChange"
        @page-size-change="onPageSizeChange"
      >
        <template #columns>
          <a-table-column title="来源ID" data-index="data_source_id" :width="180" />
          <a-table-column title="名称" data-index="name" :width="180" />
          <a-table-column title="属性" :width="320">
            <template #cell="{ record }">
              <div v-for="[key, value] in attributeEntries(record)" :key="key" class="attr-line">
                <span class="attr-key">{{ key }}</span
                >{{ value }}
              </div>
              <span v-if="!attributeEntries(record).length" class="muted">—</span>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.status)">{{ statusLabel(record.status) }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.updated_at, displayZone) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="visible" width="min(760px, 100vw)" :title="modalTitle" :on-before-ok="submit">
        <a-form :model="form" auto-label-width>
          <a-form-item field="data_source_id" label="来源ID" required>
            <a-input v-model="form.data_source_id" :disabled="editing" placeholder="例如 binance" />
          </a-form-item>
          <a-form-item field="name" label="名称" required>
            <a-input v-model="form.name" placeholder="例如 Binance" />
          </a-form-item>
          <a-form-item field="status" label="状态">
            <a-select v-model="form.status">
              <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item v-if="attributeEntries(form).length" label="属性">
            <a-table
              :data="formAttributeRows"
              row-key="key"
              size="small"
              :pagination="false"
              :bordered="{ cell: true }"
              class="attr-table"
            >
              <template #columns>
                <a-table-column title="属性" data-index="key" :width="160" />
                <a-table-column title="值" data-index="value" />
              </template>
            </a-table>
          </a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { createSubmitScope } from "@/utils/submit-scope";
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { createDataSource, listDataSources, updateDataSource } from "@/api/storage/metadata";
import type { DataSource } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  applyPageResult,
  defaultPagination,
  formatTime,
  statusColor,
  statusLabel,
  statusOptions
} from "@/views/data/shared/metadata-utils";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
defineOptions({ name: "DataSources" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const rows = ref<DataSource[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const searchKeyword = ref("");
const pagination = reactive(defaultPagination());

const form = reactive<DataSource>({
  space_id: "",
  data_source_id: "",
  name: "",
  status: "active"
});

const modalTitle = computed(() => (editing.value ? "编辑数据源" : "新建数据源"));

const loadRequests = createLatestRequestGuard();

async function load() {
  if (!selectedSpaceId.value) {
    rows.value = [];
    return;
  }
  const request = loadRequests.begin();
  const spaceId = selectedSpaceId.value;
  loading.value = true;
  try {
    const rsp = await listDataSources({
      space_id: spaceId,
      keyword: searchKeyword.value.trim() || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    // 空间切换后旧空间迟到的响应不能覆盖新空间的列表，否则编辑保存会把旧空间的数据当成当前空间的写入。
    if (!request.isLatest() || spaceId !== selectedSpaceId.value) return;
    rows.value = rsp.data_sources || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

// 编辑弹窗里的属性小表格
const formAttributeRows = computed(() => attributeEntries(form).map(([key, value]) => ({ key, value })));

function attributeEntries(source: Pick<DataSource, "attributes">): Array<[string, string]> {
  return Object.entries(source.attributes || {});
}

function onSearch() {
  pagination.current = 1;
  void load();
}

function resetForm() {
  Object.assign(form, {
    space_id: selectedSpaceId.value,
    data_source_id: "",
    name: "",
    status: "active",
    attributes: undefined
  });
}

function openCreate() {
  editing.value = false;
  resetForm();
  visible.value = true;
}

function openEdit(record: DataSource) {
  editing.value = true;
  Object.assign(form, { attributes: undefined, created_at: undefined, updated_at: undefined }, record);
  visible.value = true;
}

/** 弹窗的“确定”：校验或保存失败时返回 false，弹窗保持打开，填的内容不丢（失败原因由请求层提示）。 */
async function submit(): Promise<boolean> {
  const spaceId = spaceStore.requireSpaceId();
  // 表单属于打开它时的空间：切换空间后不能把它写进新空间（会覆盖新空间里相同 ID 的数据源）。
  if (form.space_id && form.space_id !== spaceId) {
    Message.warning("空间已切换，请重新打开编辑窗口");
    return true;
  }
  if (!form.data_source_id || !form.name) {
    Message.warning("请补全来源ID和名称");
    return false;
  }
  // 属性由写入方维护，页面不编辑但要原样带回，否则保存会把已有属性清掉。
  const payload = { ...form, space_id: spaceId };
  const submission = sourceScope.begin();
  try {
    if (editing.value) await updateDataSource(payload);
    else await createDataSource(payload);
  } catch {
    return false;
  }
  Message.success("数据源已保存");
  if (!submission.isCurrent()) return false;
  void load();
  return true;
}

function onPageChange(page: number) {
  pagination.current = page;
  load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  load();
}

watch(selectedSpaceId, () => {
  loadRequests.invalidate();
  pagination.current = 1;
  load();
});
onMounted(load);

// 提交作用域：重新打开弹窗使旧提交失效，迟到的响应不能再关闭或清空重新打开的表单
const sourceScope = createSubmitScope();
watch(visible, open => {
  if (open) sourceScope.invalidate();
});
</script>

<style scoped>
.attr-line {
  font-size: 12px;
  line-height: 1.6;
}

.attr-key {
  margin-right: 8px;
  color: var(--color-text-3);
}

.attr-table {
  width: 100%;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.muted {
  color: var(--color-text-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}
</style>
