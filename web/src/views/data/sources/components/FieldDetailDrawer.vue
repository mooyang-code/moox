<template>
  <a-drawer
    :visible="Boolean(field)"
    :width="560"
    :footer="false"
    unmount-on-close
    title="字段详情"
    @cancel="emit('close')"
    @close="emit('close')"
  >
    <template v-if="field">
      <div class="detail-head">
        <div class="detail-title">
          <strong>{{ field.display_name }}</strong>
          <code v-if="field.display_name !== field.field_id">{{ field.field_id }}</code>
        </div>
        <a-button size="small" @click="copyText(field.field_id)">
          <template #icon><icon-copy /></template>
          复制字段 ID
        </a-button>
      </div>

      <a-descriptions :column="2" bordered size="small">
        <a-descriptions-item label="字段 ID">{{ field.field_id }}</a-descriptions-item>
        <a-descriptions-item label="中文名">{{ field.display_name }}</a-descriptions-item>
        <a-descriptions-item label="来源类别">
          <a-tag size="small" :color="fieldCategoryColor(field.category)">{{ fieldCategoryLabel(field.category) }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="分组">{{ field.group }}</a-descriptions-item>
        <a-descriptions-item label="值类型">{{ fieldValueTypeLabel(field.value_type) }}</a-descriptions-item>
        <a-descriptions-item label="缺失填充">{{ fillStrategyLabel(field.fill) }}</a-descriptions-item>
        <a-descriptions-item v-if="field.aliases.length" label="字段别名" :span="2">
          {{ field.aliases.join("、") }}
        </a-descriptions-item>
      </a-descriptions>

      <section v-if="field.extra_scopes.length" class="detail-section">
        <h3>附加字段来源与适用范围</h3>
        <a-table row-key="provider_id" size="small" :bordered="{ cell: true }" :data="field.extra_scopes" :pagination="false">
          <template #columns>
            <a-table-column title="提供方" :width="180">
              <template #cell="{ record }">
                <strong>{{ record.provider_name }}</strong>
                <div class="muted mono">{{ record.provider_id }}</div>
              </template>
            </a-table-column>
            <a-table-column title="适用品种" :width="110">
              <template #cell="{ record }">{{ formatInstruments(record.instrument_types) }}</template>
            </a-table-column>
            <a-table-column title="适用频率">
              <template #cell="{ record }">{{ record.frequencies.join("、") || "—" }}</template>
            </a-table-column>
          </template>
        </a-table>
      </section>

      <section class="detail-section">
        <h3>当前空间已落地数据集（{{ field.datasets.length }}）</h3>
        <a-table
          v-if="field.datasets.length"
          row-key="dataset_id"
          size="small"
          :bordered="{ cell: true }"
          :data="field.datasets"
          :pagination="false"
        >
          <template #columns>
            <a-table-column title="数据集" :width="220">
              <template #cell="{ record }">
                <strong>{{ record.dataset_name }}</strong>
                <div class="muted mono">{{ record.dataset_id }}</div>
              </template>
            </a-table-column>
            <a-table-column title="类型 / 频率" :width="110">
              <template #cell="{ record }">
                {{ record.data_kind === "record" ? "记录型" : record.freq ? `时序 · ${record.freq}` : "时序" }}
              </template>
            </a-table-column>
            <a-table-column title="列状态" :width="90">
              <template #cell="{ record }">
                <a-tag size="small" :color="statusColor(record.column_status)">{{ statusLabel(record.column_status) }}</a-tag>
              </template>
            </a-table-column>
          </template>
        </a-table>
        <p v-else class="detail-empty">当前空间暂无数据集包含此列；可在新建 K 线采集任务时按需勾选。</p>
      </section>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { copyText } from "@/utils/clipboard";
import { fieldValueTypeLabel, fillStrategyLabel, statusColor, statusLabel } from "@/views/data/shared/metadata-utils";
import { marketLabel } from "@/views/data/shared/series-market";
import { fieldCategoryColor, fieldCategoryLabel, type CatalogFieldItem } from "../field-catalog";

defineOptions({ name: "FieldDetailDrawer" });

defineProps<{
  field: CatalogFieldItem | null;
}>();

const emit = defineEmits<{
  close: [];
}>();

function formatInstruments(types: string[]) {
  return types.map(marketLabel).filter(Boolean).join("、") || "—";
}
</script>

<style scoped>
.detail-head {
  display: flex;
  margin-bottom: var(--moox-space-3);
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
}

.detail-title {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: var(--moox-space-2);
}

.detail-title strong {
  font-size: 16px;
}

.detail-title code,
.mono {
  color: var(--color-text-3);
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}

.detail-section {
  margin-top: var(--moox-space-4);
}

.detail-section h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 14px;
  font-weight: 600;
}

.detail-empty,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
</style>
