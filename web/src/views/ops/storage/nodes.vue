<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <div class="title-with-info">
          <h2>数据节点</h2>
          <a-tooltip v-model:popup-visible="nodeInfoVisible" position="right">
            <template #content>
              <div class="node-concepts">
                <div>节点身份和服务目标由部署流程拥有，管理台只能修改名称和状态。</div>
                <div>Dataset 直接绑定 DataNode，不再经过独立路由层。</div>
                <div>Dataset 首次激活后会锁定绑定，不能再迁移。</div>
                <div>只有已禁用且没有 Dataset 的节点才能删除。</div>
              </div>
            </template>
            <button
              class="info-button"
              type="button"
              aria-label="数据节点说明"
              @focus="nodeInfoVisible = true"
              @blur="nodeInfoVisible = false"
            >
              <icon-info-circle />
            </button>
          </a-tooltip>
        </div>
      </div>

      <a-table
        row-key="node.node_id"
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
          <a-table-column title="节点ID" :width="190">
            <template #cell="{ record }">
              <span class="readonly-value">{{ record.node.node_id }}</span>
            </template>
          </a-table-column>
          <a-table-column title="名称" :width="180">
            <template #cell="{ record }">{{ record.node.name || "-" }}</template>
          </a-table-column>
          <a-table-column title="服务目标" :width="280">
            <template #cell="{ record }">
              <span class="readonly-value">{{ record.node.service_target || "-" }}</span>
            </template>
          </a-table-column>
          <a-table-column title="Dataset" :width="280">
            <template #cell="{ record }">
              <div v-if="record.datasets?.length" class="dataset-tags">
                <a-tooltip v-for="summary in record.datasets" :key="`${summary.space_id}:${summary.dataset_id}`">
                  <template #content>Space：{{ summary.space_id }} · Dataset ID：{{ summary.dataset_id }}</template>
                  <a-tag class="dataset-tag" size="small" color="arcoblue" @click="openDataset(summary)">
                    {{ summary.name || summary.dataset_id }}
                  </a-tag>
                </a-tooltip>
              </div>
              <span v-else class="empty-value">-</span>
            </template>
          </a-table-column>
          <a-table-column title="状态" :width="90">
            <template #cell="{ record }">
              <a-tag size="small" :color="statusColor(record.node.status)">{{ record.node.status }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="更新时间" :width="180">
            <template #cell="{ record }">{{ formatTime(record.node.updated_at) }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="190" align="center" :fixed="'right'">
            <template #cell="{ record }">
              <a-space class="row-actions" :size="4" wrap>
                <a-button size="mini" type="text" @click="openDetail(record)">
                  <template #icon><icon-eye /></template>
                  查看
                </a-button>
                <a-button size="mini" type="text" @click="openEdit(record)">
                  <template #icon><icon-edit /></template>
                  编辑
                </a-button>
                <a-tooltip :content="deleteHint(record)">
                  <span class="delete-trigger">
                    <a-button
                      size="mini"
                      type="text"
                      status="danger"
                      :disabled="!canDelete(record)"
                      aria-label="删除数据节点"
                      @click="remove(record)"
                    >
                      <template #icon><icon-delete /></template>
                      删除
                    </a-button>
                  </span>
                </a-tooltip>
              </a-space>
            </template>
          </a-table-column>
        </template>
      </a-table>
    </div>

    <a-modal
      v-model:visible="visible"
      data-testid="data-node-edit-modal"
      width="620px"
      title="编辑数据节点"
      :ok-loading="submitting"
      @ok="submit"
    >
      <a-form :model="form" auto-label-width>
        <a-form-item field="node_id" label="节点ID">
          <a-input :model-value="form.node_id" disabled />
        </a-form-item>
        <a-form-item field="service_target" label="服务目标">
          <a-input :model-value="form.service_target" disabled />
        </a-form-item>
        <a-form-item field="name" label="名称" required>
          <a-input v-model="form.name" aria-label="节点名称" placeholder="节点名称" />
        </a-form-item>
        <a-form-item field="status" label="状态" required>
          <a-select v-model="form.status">
            <a-option v-for="item in statusOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
          </a-select>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer
      v-model:visible="detailVisible"
      data-testid="data-node-detail-drawer"
      width="640px"
      title="数据节点详情"
      :footer="false"
    >
      <a-descriptions v-if="detailNode" :column="{ xs: 1, sm: 2 }" bordered>
        <a-descriptions-item label="节点ID">{{ detailNode.node.node_id }}</a-descriptions-item>
        <a-descriptions-item label="名称">{{ detailNode.node.name || "-" }}</a-descriptions-item>
        <a-descriptions-item label="服务目标" :span="2">{{ detailNode.node.service_target || "-" }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag size="small" :color="statusColor(detailNode.node.status)">{{ detailNode.node.status }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="更新时间">{{ formatTime(detailNode.node.updated_at) }}</a-descriptions-item>
      </a-descriptions>

      <div v-if="detailNode" class="detail-datasets">
        <h3>Dataset</h3>
        <a-table
          v-if="detailNode.datasets.length"
          row-key="dataset_id"
          size="small"
          :bordered="{ cell: true }"
          :data="detailNode.datasets"
          :pagination="false"
          :scroll="{ x: 'max-content' }"
        >
          <template #columns>
            <a-table-column title="Space" data-index="space_id" :width="120" />
            <a-table-column title="Dataset ID" data-index="dataset_id" :width="150" />
            <a-table-column title="名称" :width="150">
              <template #cell="{ record }">{{ record.name || record.dataset_id }}</template>
            </a-table-column>
            <a-table-column title="数据类型" :width="150" data-index="data_kind" />
            <a-table-column title="保留时长" :width="120" data-index="keep_duration" />
            <a-table-column title="状态" :width="90">
              <template #cell="{ record }">
                <a-tag size="small" :color="statusColor(record.status)">{{ record.status }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="100" align="center">
              <template #cell="{ record }">
                <a-button size="mini" type="text" @click="openDataset(record)">打开</a-button>
              </template>
            </a-table-column>
          </template>
        </a-table>
        <a-empty v-else description="暂无 Dataset" />
      </div>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { useRouter } from "vue-router";
import { deleteDataNode, listDataNodes, updateDataNode } from "@/api/storage/metadata";
import type { DataNodeListItem, DatasetSummary } from "@/api/storage/types";
import { applyPageResult, defaultPagination, formatTime, statusColor, statusOptions } from "@/views/data/shared/metadata-utils";

defineOptions({ name: "OpsStorageDataNodes" });

const router = useRouter();
const rows = ref<DataNodeListItem[]>([]);
const loading = ref(false);
const submitting = ref(false);
const visible = ref(false);
const nodeInfoVisible = ref(false);
const detailVisible = ref(false);
const detailNode = ref<DataNodeListItem>();
const pagination = reactive(defaultPagination());
const form = reactive({ node_id: "", name: "", service_target: "", status: "disabled" });

const selectedNodeId = computed(() => form.node_id);

async function load() {
  loading.value = true;
  try {
    const rsp = await listDataNodes({ page: { page: pagination.current, size: pagination.pageSize } });
    rows.value = rsp.items || [];
    applyPageResult(pagination, rsp.page_result);
  } finally {
    loading.value = false;
  }
}

function openEdit(record: DataNodeListItem) {
  Object.assign(form, record.node);
  visible.value = true;
}

function openDetail(record: DataNodeListItem) {
  detailNode.value = record;
  detailVisible.value = true;
}

function canDelete(record: DataNodeListItem) {
  return record.node.status === "disabled" && record.datasets.length === 0;
}

function deleteHint(record: DataNodeListItem) {
  if (record.node.status !== "disabled") return "请先禁用节点";
  if (record.datasets.length > 0) return "请先解绑全部 Dataset";
  return "删除数据节点";
}

async function submit() {
  if (!selectedNodeId.value || !form.name.trim()) {
    Message.warning("名称不能为空");
    return;
  }
  submitting.value = true;
  try {
    await updateDataNode({ node_id: form.node_id, name: form.name.trim(), status: form.status });
    Message.success("数据节点已保存");
    visible.value = false;
    await load();
  } finally {
    submitting.value = false;
  }
}

async function remove(record: DataNodeListItem) {
  if (!canDelete(record)) return;
  await deleteDataNode({ node_id: record.node.node_id });
  Message.success("数据节点已删除");
  await load();
}

function openDataset(summary: DatasetSummary) {
  void router.push({ path: "/data/datasets", query: { space_id: summary.space_id, dataset_id: summary.dataset_id } });
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

onMounted(load);
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.title-with-info {
  display: inline-flex;
  align-items: center;
  gap: var(--moox-space-1);
}

.title-with-info h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.info-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--color-text-3);
  cursor: help;
  border: 0;
  background: transparent;
}

.node-concepts {
  max-width: 360px;
  line-height: 1.7;
}

.dataset-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
}

.dataset-tag {
  max-width: 250px;
  cursor: pointer;
}

.dataset-tag :deep(.arco-tag-content) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.readonly-value {
  color: var(--color-text-2);
  overflow-wrap: anywhere;
}

.empty-value {
  color: var(--color-text-3);
}

.row-actions {
  width: 170px;
  justify-content: center;
}

.delete-trigger {
  display: inline-flex;
}

.detail-datasets {
  margin-top: 24px;
}

.detail-datasets h3 {
  margin: 0 0 12px;
  font-size: 16px;
}
</style>
