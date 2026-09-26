<template>
  <div class="tab-panel">
    <div class="tab-toolbar">
      <span class="tab-toolbar__hint">共 {{ rows.length }} 个标签</span>
      <a-space>
        <a-button :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
        <a-button type="primary" status="success" @click="openCreate">
          <template #icon><icon-plus /></template>
          新建标签
        </a-button>
      </a-space>
    </div>

    <a-table row-key="tag_id" size="small" :bordered="{ cell: true }" :loading="loading" :data="rows" :pagination="false">
      <template #columns>
        <a-table-column title="标签" :width="220">
          <template #cell="{ record }">
            <div class="tag-title">
              <strong>{{ record.tag_name }}</strong>
              <a-tag v-if="record.builtin" size="small" color="arcoblue">内置</a-tag>
            </div>
            <span class="muted">{{ record.tag_id }}</span>
          </template>
        </a-table-column>
        <a-table-column title="描述" data-index="description" :width="220" :ellipsis="true" :tooltip="true" />
        <a-table-column title="模式" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="record.mode === 'auto' ? 'blue' : 'gray'">
              {{ record.mode === "auto" ? "自动" : "手工" }}
            </a-tag>
          </template>
        </a-table-column>
        <a-table-column title="探测配置" :width="220">
          <template #cell="{ record }">
            <span v-if="record.sources?.length">{{ record.sources.join(", ") }} · {{ record.instrument_type || "-" }}</span>
            <span v-else class="muted">—</span>
          </template>
        </a-table-column>
        <a-table-column title="调度" :width="180">
          <template #cell="{ record }">
            <span>{{ record.cron || "0 * * * *" }}</span>
            <span class="muted">{{ record.timezone || "UTC" }}</span>
          </template>
        </a-table-column>
        <a-table-column title="成员" :width="110">
          <template #cell="{ record }">
            <span class="count-active">{{ record.active_count ?? 0 }}</span>
            <span class="muted"> / {{ record.inactive_count ?? 0 }}</span>
          </template>
        </a-table-column>
        <a-table-column title="最近运行" :width="190">
          <template #cell="{ record }">
            <a-tooltip v-if="record.last_status === 'failed' && record.last_error" :content="record.last_error">
              <span class="run-status run-status--failed">失败 · {{ formatTime(record.last_run_at) }}</span>
            </a-tooltip>
            <span v-else-if="record.last_status === 'success'" class="run-status run-status--success">
              成功 · {{ formatTime(record.last_run_at) }}
            </span>
            <span v-else class="muted">未运行</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="180" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openMembers(record)">成员</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-button size="mini" type="text" status="danger" :disabled="record.builtin" @click="confirmDelete(record)">
                删除
              </a-button>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <a-empty v-if="!loading && !rows.length" description="暂无标签" />

    <a-modal v-model:visible="visible" width="700px" :title="editing ? '编辑标签' : '新建标签'" @before-ok="submit">
      <a-form :model="form" auto-label-width>
        <a-form-item field="tag_id" label="标签 ID" required>
          <a-input v-model="form.tag_id" :disabled="editing" placeholder="例如 binance_spot" />
        </a-form-item>
        <a-form-item field="tag_name" label="标签名称" required>
          <a-input v-model="form.tag_name" placeholder="例如 币安现货" />
        </a-form-item>
        <a-form-item field="description" label="描述">
          <a-textarea v-model="form.description" :auto-size="{ minRows: 2, maxRows: 4 }" allow-clear />
        </a-form-item>
        <a-form-item field="mode" label="模式" required>
          <a-radio-group v-model="form.mode" :disabled="editing && Boolean(activeTag?.builtin)">
            <a-radio value="manual">手工</a-radio>
            <a-radio value="auto">自动</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="form.mode === 'manual'" field="probe" label="自动探测有效性">
          <a-switch v-model="form.probe" />
        </a-form-item>
        <template v-if="needsSource">
          <a-form-item field="sources" label="数据源" required>
            <a-select v-model="form.sources" multiple allow-search placeholder="选择标的列表数据源">
              <a-option v-for="source in listingSources" :key="source.data_source_id" :value="source.data_source_id">
                {{ source.name || source.data_source_id }}（{{ source.data_source_id }}）
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item field="instrument_type" label="产品类型" required>
            <a-select v-model="form.instrument_type" placeholder="选择产品类型">
              <a-option v-for="type in instrumentTypes" :key="type" :value="type">{{ type }}</a-option>
            </a-select>
          </a-form-item>
          <a-row :gutter="16">
            <a-col :span="12">
              <a-form-item field="cron" label="Cron" required>
                <a-input v-model="form.cron" placeholder="0 * * * *" />
              </a-form-item>
            </a-col>
            <a-col :span="12">
              <a-form-item field="timezone" label="时区" required>
                <a-select v-model="form.timezone" allow-search allow-create>
                  <a-option v-for="timezone in timezoneOptions" :key="timezone" :value="timezone">{{ timezone }}</a-option>
                </a-select>
              </a-form-item>
            </a-col>
          </a-row>
          <div class="next-runs">
            <span class="muted">接下来运行</span>
            <a-tag v-for="run in previewRuns" :key="run" size="small">{{ formatTime(run) }}</a-tag>
            <span v-if="!previewRuns.length" class="run-status--failed">Cron 表达式无效</span>
          </div>
        </template>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="referenceVisible" title="标签正在被使用" :footer="false" width="620px">
      <a-alert type="warning" show-icon>请先移除以下采集任务或数据集中的标签引用。</a-alert>
      <a-table class="reference-table" :data="references" :pagination="false" size="small" :bordered="{ cell: true }">
        <template #columns>
          <a-table-column title="数据集 ID" data-index="dataset_id" />
          <a-table-column title="数据集名称" data-index="dataset_name" />
          <a-table-column title="采集任务 ID" data-index="collector_task_id" />
        </template>
      </a-table>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { deleteTag, listDataSources, listTags, upsertTag } from "@/api/storage/metadata";
import type { DataSource, Tag, TagReference } from "@/api/storage/types";
import { defaultPagination, formatTime } from "@/views/data/shared/metadata-utils";
import { nextRuns, tagToFormState, toTagPayload, validateTagForm, type TagFormState } from "./tag-form";

const props = defineProps<{ spaceId: string }>();
const emit = defineEmits<{ (event: "openMembers", tagId: string): void }>();

const rows = ref<Tag[]>([]);
const dataSources = ref<DataSource[]>([]);
const loading = ref(false);
const visible = ref(false);
const editing = ref(false);
const activeTag = ref<Tag>();
const pagination = reactive(defaultPagination());
const referenceVisible = ref(false);
const references = ref<TagReference[]>([]);
const form = reactive<TagFormState>(emptyForm());

const needsSource = computed(() => form.mode === "auto" || form.probe);
const listingSources = computed(() => dataSources.value.filter(source => parseListing(source).length > 0));
const instrumentTypes = computed(() => {
  const values = new Set<string>();
  for (const source of listingSources.value) parseListing(source).forEach(item => values.add(item));
  return [...values];
});
const previewRuns = computed(() => (needsSource.value ? nextRuns(form.cron, form.timezone, 3) : []));
const timezoneOptions = ["UTC", "Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "America/New_York", "Europe/London"];

function emptyForm(): TagFormState {
  return {
    tag_id: "",
    tag_name: "",
    description: "",
    mode: "manual",
    probe: false,
    sources: [],
    instrument_type: "",
    cron: "0 * * * *",
    timezone: "UTC"
  };
}

function parseListing(source: DataSource): string[] {
  try {
    const value = source.attributes?.subject_listing;
    const parsed = value ? JSON.parse(value) : undefined;
    return Array.isArray(parsed?.instrument_types) ? parsed.instrument_types.map(String).filter(Boolean) : [];
  } catch {
    return [];
  }
}

async function load() {
  if (!props.spaceId) return;
  loading.value = true;
  try {
    const [tagRsp, sourceRsp] = await Promise.all([
      listTags(props.spaceId, { page: pagination.current, size: 200 }),
      listDataSources({ space_id: props.spaceId, page: { page: 1, size: 200 } })
    ]);
    rows.value = tagRsp.tags || [];
    dataSources.value = sourceRsp.data_sources || [];
    pagination.total = tagRsp.page_result?.total ?? rows.value.length;
  } finally {
    loading.value = false;
  }
}

function openMembers(tag: Tag) {
  emit("openMembers", tag.tag_id);
}

function openCreate() {
  editing.value = false;
  activeTag.value = undefined;
  Object.assign(form, emptyForm());
  visible.value = true;
}

function openEdit(tag: Tag) {
  editing.value = true;
  activeTag.value = tag;
  Object.assign(form, tagToFormState(tag));
  visible.value = true;
}

async function submit(): Promise<boolean> {
  const error = validateTagForm(form, !editing.value);
  if (error) {
    Message.warning(error);
    return false;
  }
  await upsertTag(toTagPayload(props.spaceId, form));
  Message.success("标签已保存");
  visible.value = false;
  await load();
  return true;
}

function confirmDelete(tag: Tag) {
  Modal.confirm({
    title: `删除标签“${tag.tag_name}”？`,
    content: "删除后标签成员关系也会被移除，且无法恢复。",
    onOk: async () => {
      try {
        await deleteTag(props.spaceId, tag.tag_id);
        Message.success("标签已删除");
        await load();
      } catch (error) {
        const items = (error as { response?: { data?: { references?: TagReference[] } } }).response?.data?.references || [];
        if (items.length) {
          references.value = items;
          referenceVisible.value = true;
        }
      }
    }
  });
}

watch(
  () => props.spaceId,
  () => {
    pagination.current = 1;
    void load();
  },
  { immediate: true }
);

onMounted(() => void load());
</script>

<style scoped>
.tab-panel {
  min-width: 0;
}

.tab-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.tab-toolbar__hint,
.muted {
  color: var(--color-text-3);
}

.tag-title,
.next-runs {
  display: flex;
  align-items: center;
  gap: 6px;
}

.tag-title strong {
  max-width: 150px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.muted {
  display: block;
  font-size: 12px;
}

.count-active,
.run-status--success {
  color: rgb(var(--green-6));
}

.run-status--failed {
  color: rgb(var(--red-6));
}

.next-runs {
  flex-wrap: wrap;
  margin: -4px 0 16px 96px;
}

.reference-table {
  margin-top: var(--moox-space-3);
}
</style>
