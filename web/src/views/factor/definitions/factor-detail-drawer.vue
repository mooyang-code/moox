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
        <a-space class="detail-actions">
          <a-button size="small" type="primary" @click="emit('edit', factor.factor_id)">编辑</a-button>
          <a-button size="small" @click="copyAsNew">复制为新因子</a-button>
        </a-space>
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="因子 ID">{{ factor.factor_id }}</a-descriptions-item>
          <a-descriptions-item label="展示名">{{ factor.name }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ factorTypeLabel(factor.factor_type) }}</a-descriptions-item>
          <a-descriptions-item label="分类">{{ categoryName(props.categories ?? [], factor.category_id) }}</a-descriptions-item>
          <a-descriptions-item label="最大回看周期">{{ factor.lookback_periods }}</a-descriptions-item>
          <a-descriptions-item v-if="factor.factor_type === 'cross_section'" label="允许部分对象" :span="2">
            {{ factor.allow_partial_universe ? "是" : "否" }}
          </a-descriptions-item>
          <a-descriptions-item label="输入列" :span="2">{{ factor.input_columns.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="输出列" :span="2">{{ factor.outputs.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="可调参数" :span="2">
            <ul v-if="factor.params?.length" class="param-list">
              <li v-for="param in factor.params" :key="param.name">{{ describeParam(param) }}</li>
            </ul>
            <span v-else>{{ describeParams(factor.params) }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="定义指纹" :span="2">
            <span class="source-hash">{{ factor.definition_hash || "-" }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="原文 Hash" :span="2">
            <span class="source-hash">{{ factor.spec_hash || "-" }}</span>
          </a-descriptions-item>
          <a-descriptions-item label="更新时间" :span="2">{{ formatTime(factor.updated_at, displayZone) }}</a-descriptions-item>
        </a-descriptions>

        <section class="detail-section">
          <h3>使用情况</h3>
          <span v-if="!chips.length" class="detail-empty">{{
            otherUsage ? `只有其他空间在用：${otherUsage} 个成员` : "未被使用"
          }}</span>
          <ul v-else class="usage-list">
            <li v-for="chip in chips" :key="chip.key">
              <a-link @click="emit('openTask', chip.taskId)">{{ chip.label }}</a-link>
              <span class="usage-meta"
                >{{ chip.memberKey ? ` · ${chip.memberKey}` : "" }}{{ chip.paramsText ? ` · ${chip.paramsText}` : "" }} ·
                {{ chip.statusLabel }}</span
              >
              <a-link @click="openResults(chip.taskId)">结果</a-link>
            </li>
          </ul>
        </section>

        <section class="detail-section">
          <h3>表达式定义</h3>
          <AsyncCodeEditor
            :model-value="factor.spec || ''"
            language="yaml"
            read-only
            :min-lines="6"
            :max-lines="28"
            aria-label="因子表达式定义（只读）"
          />
        </section>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { copyFactorId, formFromDefinition, setPendingCopy } from "@/views/factor/editor/factor-form";
import { getFactor } from "@/api/factor";
import type { FactorCategory, FactorDef, FactorUsage } from "@/api/factor/types";
import { categoryName, describeParam, describeParams } from "@/views/factor/shared/params";
import { AsyncCodeEditor } from "@/components/code-editor/async";
import { useFactorStore } from "@/store/modules/factor";
import { RequestGate } from "@/utils/request-gate";
import { formatTime } from "@/views/data/shared/metadata-utils";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { usageChips } from "./definitions-model";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 页面时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
defineOptions({ name: "FactorDetailDrawer" });

const props = defineProps<{ factorId: string; categories?: FactorCategory[] }>();
const emit = defineEmits<{ (e: "close"): void; (e: "openTask", taskId: string): void; (e: "edit", factorId: string): void }>();
const router = useRouter();

/** 复制为新因子：带着这个定义进入新建页，ID 预填 <原 ID>_v2。 */
function copyAsNew() {
  if (!factor.value) return;
  setPendingCopy({ ...formFromDefinition(factor.value), factor_id: copyFactorId(factor.value.factor_id) });
  void router.push("/factor/definitions/new");
}

function openResults(taskId: string) {
  void router.push({ path: "/factor/tasks", query: { tab: "results", task: taskId } });
}

const store = useFactorStore();
const factor = ref<FactorDef | null>(null);
const usages = ref<FactorUsage[]>([]);
// 其他空间引用这个因子的成员数（只给数量）
const otherUsage = ref(0);
const loading = ref(false);
const error = ref("");
const gate = new RequestGate();

const chips = computed(() =>
  usageChips(usages.value, taskId => {
    const info = store.tasks.find((item: { factor_task: { task_id: string } }) => item.factor_task.task_id === taskId);
    return info ? store.taskLabel(info.factor_task) : taskId;
  })
);

async function load(factorId: string) {
  const token = gate.next();
  factor.value = null;
  usages.value = [];
  otherUsage.value = 0;
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
    otherUsage.value = rsp.other_space_usage_count ?? 0;
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

.detail-actions {
  margin-bottom: 12px;
}

.usage-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.usage-list li {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
  font-size: 13px;
}

.usage-meta {
  color: var(--color-text-3);
}

.detail-section h3 {
  margin: 0 0 8px;
  font-size: 14px;
}

.detail-empty {
  color: var(--color-text-3);
}

.param-list {
  margin: 0;
  padding-left: 18px;
}

.source-hash {
  overflow-wrap: anywhere;
  color: var(--color-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}
</style>
