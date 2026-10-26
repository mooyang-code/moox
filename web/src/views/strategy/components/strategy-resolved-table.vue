<template>
  <div>
    <a-empty v-if="!binding" :description="emptyText || '实例尚未启用，启用时才会解析绑定'" />
    <template v-else>
      <a-descriptions :column="{ xs: 1, sm: 3 }" bordered size="small" class="binding-summary">
        <a-descriptions-item label="结果 View"
          ><span class="mono">{{ binding.view_id }}</span></a-descriptions-item
        >
        <a-descriptions-item label="周期 / 日历">{{ binding.bar }} · {{ binding.calendar }}</a-descriptions-item>
        <a-descriptions-item label="市场类型"
          ><a-tag size="small" :color="binding.spot ? 'blue' : 'orange'">{{
            binding.market_type || (binding.spot ? "spot" : "swap")
          }}</a-tag></a-descriptions-item
        >
        <a-descriptions-item label="数据集"
          ><span class="mono">{{ binding.dataset_id }}</span></a-descriptions-item
        >
        <a-descriptions-item label="源数据集"
          ><span class="mono">{{ binding.source_dataset_id || "-" }}</span></a-descriptions-item
        >
        <a-descriptions-item label="上市年龄">{{
          binding.min_age_bars ? `${binding.min_age_bars} 根` : "不限"
        }}</a-descriptions-item>
      </a-descriptions>
      <a-table row-key="name" size="small" :data="binding.columns" :pagination="false" :scroll="{ x: 640 }">
        <template #columns>
          <a-table-column title="列" data-index="name" :width="220"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.name }}</span></template
            ></a-table-column
          >
          <a-table-column title="来源" :width="100"
            ><template #cell="{ record }"
              ><a-tag size="small" :color="record.source === 'factor' ? 'purple' : 'gray'">{{
                record.source === "factor" ? "因子" : "源列"
              }}</a-tag></template
            ></a-table-column
          >
          <a-table-column title="因子" :width="240"
            ><template #cell="{ record }">{{ factorText(record) }}</template></a-table-column
          >
          <a-table-column title="定义指纹" :width="160"
            ><template #cell="{ record }"
              ><a-tooltip v-if="record.definition_hash" :content="record.definition_hash"
                ><span class="mono">{{ shortHash(record.definition_hash) }}</span></a-tooltip
              ><span v-else>-</span></template
            ></a-table-column
          >
        </template>
      </a-table>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { parseResolved, shortHash } from "@/views/strategy/model";

/** 因子列的来源：成员键（与定义不同时带上定义 ID，可变成员标注“可变”）和输出名。 */
function factorText(column: { member?: string; member_kind?: string; factor_id: string; output_name: string }) {
  if (!column.factor_id && !column.member) return "-";
  const member = column.member || column.factor_id;
  const definition = column.factor_id && column.factor_id !== member ? `（定义 ${column.factor_id}）` : "";
  const kind = column.member_kind === "var" ? " [可变]" : "";
  return `${member}${definition}${kind}${column.output_name ? ` · ${column.output_name}` : ""}`;
}
const props = defineProps<{ resolvedJson: string; emptyText?: string }>();
const binding = computed(() => parseResolved(props.resolvedJson));
</script>

<style scoped>
.binding-summary {
  margin-bottom: 12px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
