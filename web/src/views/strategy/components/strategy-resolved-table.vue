<template>
  <div>
    <a-empty v-if="!binding" :description="emptyText || '实例尚未启用，启用时才会解析绑定'" />
    <template v-else>
      <a-descriptions :column="{ xs: 1, sm: 3 }" bordered size="small" class="binding-summary">
        <a-descriptions-item label="周期 / 日历">{{ binding.bar }} · {{ binding.calendar }}</a-descriptions-item>
        <a-descriptions-item label="市场模式">{{ modeText }}</a-descriptions-item>
        <a-descriptions-item label="上市年龄">{{
          binding.min_age_bars ? `${binding.min_age_bars} 根` : "不限"
        }}</a-descriptions-item>
      </a-descriptions>
      <a-table row-key="market" size="small" :data="binding.tasks" :pagination="false" class="binding-tasks">
        <template #columns>
          <a-table-column title="市场" :width="80"
            ><template #cell="{ record }"
              ><a-tag size="small" :color="record.market === 'swap' ? 'orange' : 'blue'">{{
                marketLabel(record.market)
              }}</a-tag></template
            ></a-table-column
          >
          <a-table-column title="计算任务" :width="180"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.factor_task_id }}</span></template
            ></a-table-column
          >
          <a-table-column title="结果 View" :width="200"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.view_id }}</span></template
            ></a-table-column
          >
          <a-table-column title="数据集 / 源数据集"
            ><template #cell="{ record }"
              ><span class="mono">{{ record.dataset_id }} / {{ record.source_dataset_id || "-" }}</span></template
            ></a-table-column
          >
        </template>
      </a-table>
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
import { marketLabel } from "@/views/strategy/market-mode";
import { parseResolved, shortHash } from "@/views/strategy/model";

/** 因子列的来源：成员键（与定义不同时带上定义 ID，可变成员标注“可变”）和输出名。 */
function factorText(column: { member?: string; member_kind?: string; factor_id: string; output_name: string }) {
  if (!column.factor_id && !column.member) return "-";
  const member = column.member || column.factor_id;
  const definition = column.factor_id && column.factor_id !== member ? `（定义 ${column.factor_id}）` : "";
  const kind = column.member_kind === "var" ? " [可变]" : "";
  return `${member}${definition}${kind}${column.output_name ? ` · ${column.output_name}` : ""}`;
}
// pool：这是策略池的绑定。池的市场模式只表示涉及哪些市场（成员市场的并集），选币和下单按各成员自己的 market。
const props = defineProps<{ resolvedJson: string; emptyText?: string; pool?: boolean }>();
const binding = computed(() => parseResolved(props.resolvedJson));
// 市场模式：单市场写市场名；混合市场写 从哪里选、优先在哪里下单。
const modeText = computed(() => {
  const market = binding.value?.market;
  if (!market?.select) return "-";
  if (props.pool) {
    const markets = market.select === "both" ? "现货、永续" : marketLabel(market.select);
    return `成员市场的并集：${markets}（各成员按自己的 market 选币、下单）`;
  }
  if (market.select === market.prefer || !market.prefer) return marketLabel(market.select);
  const select = market.select === "both" ? "现货、永续" : marketLabel(market.select);
  return `${select}中选币，优先在${marketLabel(market.prefer)}下单`;
});
</script>

<style scoped>
.binding-summary {
  margin-bottom: 12px;
}
.binding-tasks {
  margin-bottom: 12px;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
</style>
