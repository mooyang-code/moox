<template>
  <a-drawer
    :visible="Boolean(props.info)"
    :width="700"
    :footer="false"
    unmount-on-close
    :title="title"
    @cancel="emit('close')"
    @close="emit('close')"
  >
    <template v-if="props.info">
      <div class="detail-head">
        <a-tag :color="statusTag.color">{{ statusTag.label }}</a-tag>
        <HealthTag v-if="health.key !== set.status" :set="set" :last-run="props.info.last_run" />
        <span class="detail-head__id">{{ set.set_id }}</span>
        <a-space class="detail-head__actions" wrap>
          <a-button v-if="actionKind === 'retry'" size="small" type="primary" :loading="props.busy" @click="emit('retry', set)">
            重试激活
          </a-button>
          <a-popconfirm
            v-if="actionKind === 'disable'"
            content="停用后不再接收周期事件；结果数据集和 View 保留，确认停用？"
            @ok="emit('toggleStatus', set, 'disabled')"
          >
            <a-button size="small" status="danger" :loading="props.busy">停用</a-button>
          </a-popconfirm>
          <a-button
            v-if="actionKind === 'enable'"
            size="small"
            type="primary"
            status="success"
            :loading="props.busy"
            @click="emit('toggleStatus', set, 'enabled')"
          >
            启用
          </a-button>
        </a-space>
      </div>

      <section class="detail-section">
        <h3>基本信息</h3>
        <a-descriptions :column="2" bordered size="small">
          <a-descriptions-item label="计算任务 ID">{{ set.set_id }}</a-descriptions-item>
          <a-descriptions-item label="频率">{{ set.freq }}</a-descriptions-item>
          <a-descriptions-item label="源数据集" :span="2">{{ sourceName }}（{{ set.source_dataset_id }}）</a-descriptions-item>
          <a-descriptions-item label="对象范围" :span="2">
            {{ scopeText(set) }}
            <span v-if="set.subject_mode === 'include'" class="detail-subjects">{{ set.subjects.join(", ") }}</span>
            <a-button size="mini" type="text" :disabled="locked" @click="emit('editScope', set)">修改</a-button>
          </a-descriptions-item>
          <a-descriptions-item label="结果数据集" :span="2">{{ set.result_dataset_id }}</a-descriptions-item>
          <a-descriptions-item label="结果视图" :span="2">{{ resultViewId || "-" }}</a-descriptions-item>
          <a-descriptions-item label="创建时间">{{ formatTime(set.created_at) }}</a-descriptions-item>
          <a-descriptions-item label="更新时间">{{ formatTime(set.updated_at) }}</a-descriptions-item>
        </a-descriptions>
      </section>

      <MemberSection class="detail-section" :info="props.info" :title="title" />

      <section class="detail-section">
        <h3>前往</h3>
        <a-space wrap>
          <a-button size="small" @click="goDefinitions">因子定义</a-button>
          <a-button size="small" @click="goTab('results')">计算结果</a-button>
          <a-button size="small" @click="goTab('recalc')">补算</a-button>
        </a-space>
      </section>

      <section class="detail-section danger-zone">
        <h3>危险区</h3>
        <a-tooltip :content="removeState.reason" :disabled="!removeState.disabled">
          <span>
            <a-button status="danger" :disabled="removeState.disabled" @click="emit('delete', set)">删除计算任务…</a-button>
          </span>
        </a-tooltip>
        <p class="detail-hint">删除前需要先移除全部因子；可选择同时清理结果数据集（purge，不可恢复）。</p>
      </section>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRouter } from "vue-router";
import type { FactorSet, FactorSetInfo } from "@/api/factor/types";
import { listViews } from "@/api/storage/metadata";
import { useFactorStore } from "@/store/modules/factor";
import { RequestGate } from "@/utils/request-gate";
import { setHealth } from "@/views/factor/shared/health";
import HealthTag from "@/views/factor/shared/health-tag.vue";
import { setStatusTag } from "@/views/factor/shared/status";
import { deleteState, scopeText, setActionKind } from "./compute-tasks-model";
import MemberSection from "./member-section.vue";

defineOptions({ name: "FactorTaskDetailDrawer" });

const props = defineProps<{ info?: FactorSetInfo; busy?: boolean }>();
const emit = defineEmits<{
  (e: "close"): void;
  (e: "retry", set: FactorSet): void;
  (e: "toggleStatus", set: FactorSet, next: "enabled" | "disabled"): void;
  (e: "editScope", set: FactorSet): void;
  (e: "delete", set: FactorSet): void;
}>();

const router = useRouter();
const store = useFactorStore();

const set = computed(() => props.info?.factor_set as FactorSet);
const title = computed(() => (props.info ? store.setLabel(set.value) : ""));
const statusTag = computed(() => setStatusTag(set.value?.status || ""));
const health = computed(() => setHealth(set.value ?? { status: "pending", freq: "" }, props.info?.last_run));
const actionKind = computed(() => (set.value ? setActionKind(set.value) : "none"));
const locked = computed(() => set.value?.status === "deleting" || set.value?.status === "pending");
const removeState = computed(() => (props.info ? deleteState(props.info) : { disabled: true, reason: "" }));
const sourceName = computed(() =>
  set.value ? store.datasetNames[set.value.source_dataset_id] || set.value.source_dataset_id : ""
);
const resultViewId = ref("");
const viewGate = new RequestGate();

async function loadResultView(current?: FactorSet) {
  const token = viewGate.next();
  resultViewId.value = "";
  if (!current) return;
  try {
    const rsp = await listViews({
      space_id: current.space_id,
      dataset_id: current.result_dataset_id,
      status: "active",
      page: { page: 1, size: 100 }
    });
    if (!viewGate.isCurrent(token)) return;
    const view = (rsp.views || []).find(
      item => item.attributes?.owner_module === "factor" && item.attributes?.view_role === "factor_result"
    );
    resultViewId.value = view?.view_id || "";
  } catch {
    // 结果视图只是展示信息，失败时显示 “-”。
  }
}

watch(
  () => props.info?.factor_set.set_id,
  () => void loadResultView(props.info?.factor_set),
  { immediate: true }
);

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}

function goDefinitions() {
  void router.push("/factor/definitions");
}

function goTab(tab: "results" | "recalc") {
  void router.push({ path: "/factor/tasks", query: { tab, set: set.value.set_id } });
}
</script>

<style scoped>
.detail-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.detail-head__id {
  color: var(--color-text-3);
  font-size: 12px;
}

.detail-head__actions {
  margin-left: auto;
}

.detail-section {
  margin-top: var(--moox-space-4);
}

.detail-section h3 {
  margin: 0 0 var(--moox-space-2);
  font-size: 15px;
  font-weight: 600;
}

.detail-subjects {
  margin-left: var(--moox-space-2);
  color: var(--color-text-3);
}

.detail-hint {
  margin: var(--moox-space-2) 0 0;
  color: var(--color-text-3);
  font-size: 12px;
}

.danger-zone {
  padding-top: var(--moox-space-3);
  border-top: 1px dashed var(--color-border-2);
}
</style>
