<template>
  <div class="sources-tab">
    <div class="page-head">
      <a-input-search
        v-model="searchKeyword"
        allow-clear
        placeholder="搜索来源ID或名称"
        style="width: 280px"
        @search="onSearch"
        @clear="onSearch"
      />
      <a-button type="primary" status="success" :disabled="!spaceId" @click="openCreate">
        <template #icon><icon-plus /></template>
        新建来源
      </a-button>
    </div>

    <a-table
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
          <template #cell="{ record }">{{ formatInstant(record.updated_at, displayZone) }}</template>
        </a-table-column>
        <a-table-column title="操作" :width="90" align="center" :fixed="'right'">
          <template #cell="{ record }">
            <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <section v-if="spaceId && extraFieldSources.length" class="extra-sources">
      <h3>附加字段来源</h3>
      <p class="extra-sources__hint">
        这些来源只给 K 线补充字段，不单独建数据集；新建 K
        线采集任务时在“输出字段”里勾选，默认不勾。点击可前往「字段定义」查看全部字段详情。
      </p>
      <a-table
        row-key="provider_id"
        size="small"
        :bordered="{ cell: true }"
        :data="extraFieldSources"
        :pagination="false"
        :scroll="{ x: 'max-content' }"
      >
        <template #columns>
          <a-table-column title="来源ID" data-index="provider_id" :width="180" />
          <a-table-column title="名称" :width="200">
            <template #cell="{ record }">
              <a-link @click="emit('openFields', record.provider_id)">{{ record.name }}</a-link>
            </template>
          </a-table-column>
          <a-table-column title="适用品种" data-index="instruments" :width="110" />
          <a-table-column title="适用频率" data-index="frequencies" :width="120" />
          <a-table-column title="提供的字段" :width="420">
            <template #cell="{ record }">
              <div class="extra-fields-summary">
                <span class="extra-fields-preview">{{ previewFields(record) }}</span>
                <a-link @click="emit('openFields', record.provider_id)">共 {{ record.fields.length }} 个字段 · 查看 →</a-link>
              </div>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </section>

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
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import { GetDataTypeConfigs } from "@/api/collector";
import { reportError } from "@/api/http";
import { listDataSources, upsertDataSource } from "@/api/storage/metadata";
import type { DataSource } from "@/api/storage/types";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { createSubmitScope } from "@/utils/submit-scope";
import { formatInstant } from "@/utils/time-zone";
import { applyPageResult, defaultPagination, statusColor, statusLabel, statusOptions } from "@/views/data/shared/metadata-utils";
import { buildExtraFieldSources, type ExtraFieldSourceRow } from "./extra-field-sources";

defineOptions({ name: "DataSourcesTab" });

const props = defineProps<{ spaceId: string }>();
const emit = defineEmits<{ openFields: [providerId: string] }>();

const displayZone = useDisplayZone();
const spaceStore = useSpaceStore();
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
  if (!props.spaceId) {
    rows.value = [];
    return;
  }
  const request = loadRequests.begin();
  const currentSpace = props.spaceId;
  loading.value = true;
  try {
    const rsp = await listDataSources({
      space_id: currentSpace,
      keyword: searchKeyword.value.trim() || undefined,
      page: { page: pagination.current, size: pagination.pageSize }
    });
    if (!request.isLatest() || currentSpace !== props.spaceId) return;
    rows.value = rsp.data_sources || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

const extraFieldSources = ref<ExtraFieldSourceRow[]>([]);
const extraSourceRequests = createLatestRequestGuard();

async function loadExtraFieldSources() {
  const request = extraSourceRequests.begin();
  const currentSpace = props.spaceId;
  extraFieldSources.value = [];
  if (!currentSpace) return;
  try {
    const options = (await GetDataTypeConfigs()).configs?.find(item => item.data_type === "kline")?.extra_fields || [];
    if (!request.isLatest() || currentSpace !== props.spaceId) return;
    const mine = options.filter(option => (option.market_id || "").trim().toLowerCase() === currentSpace.trim().toLowerCase());
    if (!mine.length) return;
    extraFieldSources.value = buildExtraFieldSources(mine, currentSpace);
  } catch (error) {
    reportError(error);
  }
}

function previewFields(row: ExtraFieldSourceRow): string {
  const sample = row.fields.slice(0, 3).map(item => item.name);
  if (!sample.length) return "—";
  return row.fields.length > 3 ? `${sample.join("、")} 等` : sample.join("、");
}

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
    space_id: props.spaceId,
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

/** 弹窗的“确定”：校验或保存失败时返回 false，弹窗保持打开，填的内容不丢。 */
async function submit(): Promise<boolean> {
  const spaceId = spaceStore.requireSpaceId();
  if (form.space_id && form.space_id !== spaceId) {
    Message.warning("空间已切换，请重新打开编辑窗口");
    return true;
  }
  if (!form.data_source_id || !form.name) {
    Message.warning("请补全来源ID和名称");
    return false;
  }
  const payload = { ...form, space_id: spaceId };
  const submission = sourceScope.begin();
  try {
    await upsertDataSource(payload, !editing.value);
  } catch (error) {
    reportError(error);
    return false;
  }
  Message.success("数据源已保存");
  if (!submission.isCurrent()) return false;
  void load();
  return true;
}

function onPageChange(page: number) {
  pagination.current = page;
  void load();
}

function onPageSizeChange(pageSize: number) {
  pagination.current = 1;
  pagination.pageSize = pageSize;
  void load();
}

watch(
  () => props.spaceId,
  () => {
    loadRequests.invalidate();
    pagination.current = 1;
    void load();
    void loadExtraFieldSources();
  }
);

onMounted(() => {
  void load();
  void loadExtraFieldSources();
});

const sourceScope = createSubmitScope();
watch(visible, open => {
  if (open) sourceScope.invalidate();
});

defineExpose({ openCreate, visible, form });
</script>

<style scoped>
.page-head {
  display: flex;
  margin-bottom: var(--moox-space-2);
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.attr-line {
  font-size: 12px;
  line-height: 1.5;
}

.attr-key {
  margin-right: 6px;
  color: var(--color-text-3);
}

.attr-table {
  width: 100%;
}

.muted {
  color: var(--color-text-3);
}

.extra-sources {
  margin-top: var(--moox-space-5);
}

.extra-sources h3 {
  margin: 0 0 4px;
  font-size: 15px;
  font-weight: 600;
}

.extra-sources__hint {
  margin: 0 0 var(--moox-space-2);
  color: var(--color-text-3);
  font-size: 12px;
}

.extra-fields-summary {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.extra-fields-preview {
  color: var(--color-text-2);
}
</style>
