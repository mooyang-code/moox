<template>
  <header v-if="set" class="set-header">
    <div class="set-header__title">
      <h2>{{ set.source_dataset_id }} · {{ set.freq }}</h2>
      <a-tag :color="statusTag.color">{{ statusTag.label }}</a-tag>
      <a-tag v-if="health.key !== set.status" :color="health.color">{{ health.label }}</a-tag>
      <span class="set-header__id">{{ set.set_id }}</span>
    </div>
    <a-space wrap>
      <a-button v-if="set.status === 'pending'" type="primary" :loading="busy" @click="retryActivation">重试激活</a-button>
      <a-popconfirm
        v-if="set.status === 'enabled'"
        content="停用后不再接收周期事件；结果数据集和 View 保留，确认停用？"
        @ok="toggleStatus('disabled')"
      >
        <a-button status="danger" :loading="busy">停用</a-button>
      </a-popconfirm>
      <a-button v-if="set.status === 'disabled'" type="primary" status="success" :loading="busy" @click="toggleStatus('enabled')"
        >启用</a-button
      >
      <a-button :disabled="locked" @click="openScope">修改对象范围</a-button>
      <a-button status="danger" :disabled="set.status === 'deleting'" @click="openDelete">删除</a-button>
    </a-space>
  </header>

  <a-modal v-model:visible="scopeVisible" title="修改对象范围" :width="560" :ok-loading="busy" :on-before-ok="saveScope">
    <a-alert type="info" show-icon class="dialog-tip">从下一个周期起生效；已写入的历史需要手动补算。</a-alert>
    <a-form layout="vertical">
      <SubjectScopeFields v-model:mode="scopeMode" v-model:subjects="scopeSubjects" />
    </a-form>
  </a-modal>

  <a-modal
    v-model:visible="deleteVisible"
    title="删除因子集"
    :width="560"
    :ok-loading="busy"
    :ok-button-props="{ status: 'danger', disabled: !deleteConfirmed }"
    :on-before-ok="confirmDelete"
  >
    <a-radio-group v-model="purge" direction="vertical">
      <a-radio :value="false">仅删除因子集（保留结果数据集和已写入的数据）</a-radio>
      <a-radio :value="true">同时清理结果数据集（purge，不可恢复）</a-radio>
    </a-radio-group>
    <a-form-item v-if="purge" class="delete-confirm" :label="`请输入 ${set?.set_id} 以确认`">
      <a-input v-model="confirmText" allow-clear />
    </a-form-item>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { createFactorSet, deleteFactorSet, setFactorSetStatus, updateFactorSet } from "@/api/factor";
import type { SubjectMode } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { setHealth } from "./health";
import { setStatusTag } from "./status";
import SubjectScopeFields from "./subject-scope-fields.vue";

defineOptions({ name: "FactorSetHeader" });

const store = useFactorStore();
const set = computed(() => store.current?.factor_set);
const statusTag = computed(() => setStatusTag(set.value?.status || ""));
const health = computed(() => setHealth(set.value ?? { status: "pending", freq: "" }, store.current?.last_run));
const locked = computed(() => set.value?.status === "deleting" || set.value?.status === "pending");

const busy = ref(false);
const scopeVisible = ref(false);
const scopeMode = ref<SubjectMode>("all");
const scopeSubjects = ref<string[]>([]);
const deleteVisible = ref(false);
const purge = ref(false);
const confirmText = ref("");
const deleteConfirmed = computed(() => !purge.value || confirmText.value.trim() === set.value?.set_id);

async function run(action: () => Promise<unknown>, success: string, failure: string) {
  busy.value = true;
  try {
    await action();
    Message.success(success);
    await store.reload();
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : failure);
    return false;
  } finally {
    busy.value = false;
  }
}

function toggleStatus(next: "enabled" | "disabled") {
  const current = set.value;
  if (!current) return Promise.resolve(false);
  return run(
    () => setFactorSetStatus(current.set_id, next),
    next === "enabled" ? "因子集已启用" : "因子集已停用",
    "更新因子集状态失败"
  );
}

function retryActivation() {
  const current = set.value;
  if (!current) return Promise.resolve(false);
  return run(
    () =>
      createFactorSet({
        set_id: current.set_id,
        space_id: current.space_id,
        source_dataset_id: current.source_dataset_id,
        freq: current.freq,
        result_dataset_id: current.result_dataset_id,
        subject_mode: current.subject_mode,
        subjects: current.subjects
      }),
    "已重新提交激活",
    "重试激活失败"
  );
}

function openScope() {
  if (!set.value) return;
  scopeMode.value = set.value.subject_mode;
  scopeSubjects.value = [...(set.value.subjects || [])];
  scopeVisible.value = true;
}

async function saveScope() {
  const current = set.value;
  if (!current) return false;
  if (scopeMode.value === "include" && !scopeSubjects.value.length) {
    Message.warning("指定对象模式至少需要一个对象 ID");
    return false;
  }
  return run(
    () => updateFactorSet(current.set_id, scopeMode.value, scopeMode.value === "include" ? [...scopeSubjects.value] : []),
    "对象范围已更新",
    "更新对象范围失败"
  );
}

function openDelete() {
  purge.value = false;
  confirmText.value = "";
  deleteVisible.value = true;
}

function confirmDelete() {
  const current = set.value;
  if (!current || !deleteConfirmed.value) return false;
  return run(
    () => deleteFactorSet(current.set_id, purge.value),
    purge.value ? "因子集已删除，结果数据集清理中" : "因子集已删除",
    "删除因子集失败"
  );
}
</script>

<style scoped>
.set-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.set-header__title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
}

.set-header__title h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.set-header__id {
  color: var(--color-text-3);
  font-size: 12px;
}

.dialog-tip {
  margin-bottom: var(--moox-space-3);
}

.delete-confirm {
  margin-top: var(--moox-space-3);
}
</style>
