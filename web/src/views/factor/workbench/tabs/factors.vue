<template>
  <div v-if="info" class="factors-tab">
    <div class="factors-tab__bar">
      <span class="factors-tab__hint">编辑与删除需先停用；启用时自动加列并回填历史。</span>
      <a-button type="primary" status="success" :disabled="locked" @click="openCreate">
        <template #icon><icon-plus /></template>
        新增因子
      </a-button>
    </div>

    <a-empty
      v-if="!factors.length"
      description="该因子集还没有因子。可新增因子，或用命令行 moox-factor-cli import-catalog 导入内置因子。"
    />
    <a-table
      v-else
      row-key="factor_id"
      size="small"
      :bordered="{ cell: true }"
      :data="factors"
      :pagination="false"
      :scroll="{ x: 'max-content' }"
    >
      <template #columns>
        <a-table-column title="因子 ID" data-index="factor_id" :width="150" />
        <a-table-column title="模块名" data-index="name" :width="130" />
        <a-table-column title="类型" :width="100">
          <template #cell="{ record }">{{ factorTypeLabel(record.factor_type) }}</template>
        </a-table-column>
        <a-table-column title="输入列" :width="170" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ record.input_columns.join(", ") }}</template>
        </a-table-column>
        <a-table-column title="输出列" :width="170" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ record.outputs.join(", ") }}</template>
        </a-table-column>
        <a-table-column title="回看周期" data-index="lookback_periods" :width="90" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tag size="small" :color="factorStatusTag(record.status).color">{{ factorStatusTag(record.status).label }}</a-tag>
          </template>
        </a-table-column>
        <a-table-column title="最近周期" :width="100">
          <template #cell="{ record }">
            <template v-if="periodState(record)">
              <a-tag size="small" :color="periodStatusTag(periodState(record)!.status).color">
                {{ periodStatusTag(periodState(record)!.status).label }}
              </a-tag>
            </template>
            <span v-else>-</span>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="260" align="center" fixed="right">
          <template #cell="{ record }">
            <a-space>
              <a-button size="mini" type="text" @click="openDetail(record)">详情</a-button>
              <a-button size="mini" type="text" @click="openEdit(record)">编辑</a-button>
              <a-button
                v-if="record.status === 'enabled'"
                size="mini"
                type="text"
                status="danger"
                :loading="pending === record.factor_id"
                @click="disable(record)"
                >停用</a-button
              >
              <a-button
                v-else
                size="mini"
                type="text"
                status="success"
                :disabled="locked"
                :loading="pending === record.factor_id"
                @click="enable(record)"
                >启用</a-button
              >
              <a-popconfirm content="删除后不可恢复（已写入结果数据集的列保留），确认继续？" @ok="remove(record)">
                <a-button size="mini" type="text" status="danger" :disabled="record.status !== 'disabled'">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>

    <FactorEditor
      v-model:visible="editorVisible"
      :set="info.factor_set"
      :siblings="factors"
      :factor="editingFactor"
      @saved="onSaved"
    />

    <a-drawer v-model:visible="detailVisible" title="因子详情" :width="860">
      <template v-if="detail">
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="因子 ID">{{ detail.factor_id }}</a-descriptions-item>
          <a-descriptions-item label="模块名">{{ detail.name }}</a-descriptions-item>
          <a-descriptions-item label="类型">{{ factorTypeLabel(detail.factor_type) }}</a-descriptions-item>
          <a-descriptions-item label="状态">
            <a-tag size="small" :color="factorStatusTag(detail.status).color">{{ factorStatusTag(detail.status).label }}</a-tag>
          </a-descriptions-item>
          <a-descriptions-item label="回看周期数">{{ detail.lookback_periods }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.factor_type === 'cross_section'" label="允许部分对象">{{
            detail.allow_partial_universe ? "是" : "否"
          }}</a-descriptions-item>
          <a-descriptions-item label="输入列" :span="2">{{ detail.input_columns.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="输出列" :span="2">{{ detail.outputs.join(", ") || "-" }}</a-descriptions-item>
          <a-descriptions-item label="参数" :span="2"
            ><code>{{ detail.params_json }}</code></a-descriptions-item
          >
          <a-descriptions-item label="源码Hash" :span="2"
            ><span class="source-hash">{{ detail.source_hash || "-" }}</span></a-descriptions-item
          >
          <a-descriptions-item label="更新时间" :span="2">{{ formatTime(detail.updated_at) }}</a-descriptions-item>
        </a-descriptions>
        <div class="detail-section">
          <h3>源码</h3>
          <CodeBlock :code="detail.source_code" language="python" />
        </div>
      </template>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message, Modal } from "@arco-design/web-vue";
import { deleteFactor, setFactorStatus } from "@/api/factor";
import type { FactorDef, FactorPeriodState, FactorSetInfo } from "@/api/factor/types";
import CodeBlock from "@/components/code-block/index.vue";
import { useFactorStore } from "@/store/modules/factor";
import { formatTime } from "@/views/data/shared/metadata-utils";
import FactorEditor from "../factor-editor.vue";
import { factorStatusTag, factorTypeLabel, periodStatusTag } from "../status";

defineOptions({ name: "FactorFactorsTab" });

const store = useFactorStore();
const route = useRoute();
const router = useRouter();
const info = computed<FactorSetInfo | undefined>(() => store.current);
const factors = computed<FactorDef[]>(() => info.value?.factors || []);
const locked = computed(() => info.value?.factor_set.status === "deleting" || info.value?.factor_set.status === "pending");
const periodStates = computed(
  () => new Map<string, FactorPeriodState>((info.value?.last_run?.factors || []).map(state => [state.factor_id, state]))
);
const periodState = (factor: FactorDef) => periodStates.value.get(factor.factor_id);

const editorVisible = ref(false);
const editingFactor = ref<FactorDef | null>(null);
const detailVisible = ref(false);
const detail = ref<FactorDef | null>(null);
const pending = ref("");

function openCreate() {
  editingFactor.value = null;
  editorVisible.value = true;
}

function openEdit(factor: FactorDef) {
  if (factor.status === "enabled") {
    Modal.info({
      title: "需要先停用",
      content: `因子 ${factor.factor_id} 正在运行。修改流程为：停用 → 修改 → 启用，启用时会重新回填历史。`
    });
    return;
  }
  editingFactor.value = factor;
  editorVisible.value = true;
}

function openDetail(factor: FactorDef) {
  detail.value = factor;
  detailVisible.value = true;
}

async function onSaved() {
  await store.reload();
}

function enable(factor: FactorDef) {
  Modal.confirm({
    title: `启用因子 ${factor.factor_id}`,
    content: "将为结果数据集加列，并自动提交保留期内历史的回填任务；回填期间可在“补算”页签查看进度。",
    onOk: async () => {
      pending.value = factor.factor_id;
      try {
        const { backfill_job } = await setFactorStatus(factor.factor_id, "enabled");
        Message.success(backfill_job ? "因子已启用，已提交历史回填" : "因子已启用");
        await store.reload();
        if (backfill_job) {
          await router.replace({ path: route.path, query: { ...route.query, tab: "recalc", job: backfill_job.job_id } });
        }
      } catch (error) {
        Message.error(error instanceof Error ? error.message : "启用因子失败");
      } finally {
        pending.value = "";
      }
    }
  });
}

function disable(factor: FactorDef) {
  Modal.confirm({
    title: `停用因子 ${factor.factor_id}`,
    content: "停用后保留结果列和历史值，不再写入新周期，View 不会重建。",
    onOk: async () => {
      pending.value = factor.factor_id;
      try {
        await setFactorStatus(factor.factor_id, "disabled");
        Message.success("因子已停用");
        await store.reload();
      } catch (error) {
        Message.error(error instanceof Error ? error.message : "停用因子失败");
      } finally {
        pending.value = "";
      }
    }
  });
}

async function remove(factor: FactorDef) {
  try {
    await deleteFactor(factor.factor_id);
    Message.success("因子已删除");
    await store.reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除因子失败");
  }
}
</script>

<style scoped>
.factors-tab__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.factors-tab__hint {
  color: var(--color-text-3);
  font-size: 13px;
}

.detail-section {
  margin-top: 20px;
}

.detail-section h3 {
  margin: 0 0 8px;
  font-size: 14px;
}

.source-hash {
  overflow-wrap: anywhere;
  color: var(--color-text-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}
</style>
