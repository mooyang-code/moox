<template>
  <a-drawer
    :visible="Boolean(props.factorId)"
    :width="860"
    :footer="false"
    unmount-on-close
    title="因子详情"
    @cancel="emit('close')"
    @close="emit('close')"
  >
    <a-alert v-if="error" type="error" show-icon>{{ error }}</a-alert>
    <a-spin v-else :loading="loading" class="detail-spin">
      <template v-if="factor">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="因子 ID">{{ factor.factor_id }}</a-descriptions-item>
          <a-descriptions-item label="模块名">{{ factor.name }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ factorTypeLabel(factor.factor_type) }}</a-descriptions-item>
          <a-descriptions-item label="回看周期数">{{ factor.lookback_periods }}</a-descriptions-item>
          <a-descriptions-item v-if="factor.factor_type === 'cross_section'" label="允许部分对象" :span="2">
            {{ factor.allow_partial_universe ? "是" : "否" }}
          </a-descriptions-item>
          <a-descriptions-item label="输入列" :span="2">{{ factor.input_columns.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="输出列" :span="2">{{ factor.outputs.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="参数" :span="2">
            <code>{{ factor.params_json }}</code>
          </a-descriptions-item>
          <a-descriptions-item label="源码 Hash" :span="2">
            <span class="source-hash">{{ factor.source_hash || "-" }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="更新时间" :span="2">{{ formatTime(factor.updated_at) }}</a-descriptions-item>
        </a-descriptions>

        <section class="detail-section">
          <h3>使用情况</h3>
          <span v-if="!chips.length" class="detail-empty">未被使用</span>
          <a-space v-else wrap>
            <a-tag v-for="chip in chips" :key="chip.setId" class="usage-tag" @click="emit('openTask', chip.setId)">
              {{ chip.label }} · {{ chip.statusLabel }}
            </a-tag>
          </a-space>
        </section>

        <section class="detail-section">
          <h3>源码</h3>
          <AsyncCodeEditor
            :model-value="factor.source_code || ''"
            read-only
            :min-lines="6"
            :max-lines="28"
            aria-label="因子源码（只读）"
          />
        </section>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getFactor } from "@/api/factor";
import type { FactorDef, FactorUsage } from "@/api/factor/types";
import { AsyncCodeEditor } from "@/components/code-editor/async";
import { useFactorStore } from "@/store/modules/factor";
import { RequestGate } from "@/utils/request-gate";
import { formatTime } from "@/views/data/shared/metadata-utils";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { usageChips } from "./definitions-model";

defineOptions({ name: "FactorDetailDrawer" });

const props = defineProps<{ factorId: string }>();
const emit = defineEmits<{ (e: "close"): void; (e: "openTask", setId: string): void }>();

const store = useFactorStore();
const factor = ref<FactorDef | null>(null);
const usages = ref<FactorUsage[]>([]);
const loading = ref(false);
const error = ref("");
const gate = new RequestGate();

const chips = computed(() =>
  usageChips(usages.value, setId => {
    const info = store.sets.find((item: { factor_set: { set_id: string } }) => item.factor_set.set_id === setId);
    return info ? store.setLabel(info.factor_set) : setId;
  })
);

async function load(factorId: string) {
  const token = gate.next();
  factor.value = null;
  usages.value = [];
  error.value = "";
  if (!factorId) {
    loading.value = false;
    return;
  }
  loading.value = true;
  try {
    const rsp = await getFactor(factorId);
    if (!gate.isCurrent(token)) return;
    factor.value = rsp.factor;
    usages.value = rsp.usages || [];
  } catch (e) {
    if (gate.isCurrent(token)) error.value = e instanceof Error ? e.message : "因子详情加载失败";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

watch(
  () => props.factorId,
  id => void load(id),
  { immediate: true }
);
</script>

<style scoped>
.detail-spin {
  display: block;
  width: 100%;
}

.detail-section {
  margin-top: 20px;
}

.detail-section h3 {
  margin: 0 0 8px;
  font-size: 14px;
}

.detail-empty {
  color: var(--color-text-3);
}

.usage-tag {
  cursor: pointer;
}

.source-hash {
  overflow-wrap: anywhere;
  color: var(--color-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}
</style>
