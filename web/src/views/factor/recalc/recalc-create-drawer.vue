<template>
  <a-drawer
    v-model:visible="visible"
    :width="560"
    title="新建补算"
    unmount-on-close
    :ok-loading="submitting"
    ok-text="提交补算"
    :ok-button-props="{ disabled: Boolean(blocker) }"
    :on-before-ok="submit"
    @before-open="prepare"
  >
    <template v-if="props.info">
      <a-alert v-if="blocker" type="warning" show-icon class="drawer-alert">{{ blocker }}</a-alert>
      <a-form layout="vertical" :model="form">
        <a-form-item label="因子（仅已启用成员）" required>
          <a-select v-model="form.factor_ids" multiple allow-clear placeholder="选择要补算的因子">
            <a-option v-for="id in candidates" :key="id" :value="id">{{ id }}</a-option>
          </a-select>
        </a-form-item>

        <SubjectScopeFields v-model:mode="form.subject_mode" v-model:subjects="form.subjects" />

        <a-form-item label="开始时间" required>
          <a-date-picker v-model="form.start_time" show-time value-format="timestamp" format="YYYY-MM-DD HH:mm:ss" />
        </a-form-item>
        <a-form-item label="结束时间（不含）" required>
          <a-date-picker v-model="form.end_time" show-time value-format="timestamp" format="YYYY-MM-DD HH:mm:ss" />
        </a-form-item>
        <div class="quick-ranges" role="group" aria-label="快捷时间范围">
          <a-tag v-for="item in quickItems" :key="item.kind" class="quick-chip" @click="applyQuick(item.kind)">{{
            item.label
          }}</a-tag>
        </div>

        <a-form-item label="请求 ID" extra="同一请求 ID 重复提交是幂等的">
          <a-input v-model="form.request_id" allow-clear placeholder="留空自动生成" />
        </a-form-item>
      </a-form>
      <div class="estimate" :class="{ 'is-empty': !estimate.periods }">{{ estimate.text }}</div>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { recalcFactors } from "@/api/factor";
import type { FactorSetInfo, RecalcJob, SubjectMode } from "@/api/factor/types";
import SubjectScopeFields from "@/views/factor/compute-tasks/subject-scope-fields.vue";
import { defaultRange, enabledMemberIds, estimateRecalc, quickRange } from "./recalc-model";

defineOptions({ name: "FactorRecalcCreateDrawer" });

const props = defineProps<{ info?: FactorSetInfo }>();
const emit = defineEmits<{ (e: "submitted", job: RecalcJob): void }>();
const visible = defineModel<boolean>("visible", { default: false });

const quickItems = [
  { kind: "periods", label: "最近 100 个周期" },
  { kind: "day", label: "最近 1 天" },
  { kind: "week", label: "最近 7 天" }
] as const;

const submitting = ref(false);
const form = reactive({
  factor_ids: [] as string[],
  subject_mode: "all" as SubjectMode,
  subjects: [] as string[],
  start_time: undefined as number | undefined,
  end_time: undefined as number | undefined,
  request_id: ""
});

const freq = computed(() => props.info?.factor_set.freq || "");
const candidates = computed(() => (props.info ? enabledMemberIds(props.info) : []));
const estimate = computed(() =>
  estimateRecalc({
    freq: freq.value,
    start: form.start_time,
    end: form.end_time,
    factorCount: form.factor_ids.length,
    subjectMode: form.subject_mode,
    subjectCount: form.subjects.length
  })
);
const blocker = computed(() => {
  const set = props.info?.factor_set;
  if (!set) return "";
  if (set.status !== "enabled") return "计算任务未启用，无法提交补算；已有任务仍可查看。";
  if (!candidates.value.length) return "该计算任务没有已启用的因子，请先在「计算任务」里启用因子。";
  return "";
});

function setRange(range: { start: number; end: number } | null) {
  form.start_time = range?.start;
  form.end_time = range?.end;
}

function applyQuick(kind: "periods" | "day" | "week") {
  setRange(quickRange(freq.value, kind));
}

function prepare() {
  form.factor_ids = [...candidates.value];
  form.subject_mode = "all";
  form.subjects = [];
  form.request_id = "";
  setRange(defaultRange(freq.value));
}

async function submit() {
  const current = props.info?.factor_set;
  if (!current || blocker.value) return false;
  if (!form.factor_ids.length) {
    Message.warning("请至少选择一个因子");
    return false;
  }
  if (form.start_time === undefined || form.end_time === undefined) {
    Message.warning("请选择补算时间范围");
    return false;
  }
  if (form.end_time <= form.start_time) {
    Message.warning("结束时间必须晚于开始时间");
    return false;
  }
  if (form.subject_mode === "include" && !form.subjects.length) {
    Message.warning("指定对象模式至少需要一个对象 ID");
    return false;
  }
  submitting.value = true;
  try {
    const job = await recalcFactors({
      set_id: current.set_id,
      factor_ids: [...form.factor_ids],
      subjects: form.subject_mode === "include" ? [...form.subjects] : [],
      start_time: new Date(form.start_time).toISOString(),
      end_time: new Date(form.end_time).toISOString(),
      request_id: form.request_id.trim() || `factor-recalc-${Date.now()}`
    });
    Message.success("补算任务已受理");
    emit("submitted", job);
    return true;
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "补算任务提交失败");
    return false;
  } finally {
    submitting.value = false;
  }
}
</script>

<style scoped>
.drawer-alert {
  margin-bottom: var(--moox-space-3);
}

.quick-ranges {
  display: flex;
  flex-wrap: wrap;
  gap: var(--moox-space-2);
  margin: calc(-1 * var(--moox-space-2)) 0 var(--moox-space-3);
}

.quick-chip {
  cursor: pointer;
}

.estimate {
  padding: var(--moox-space-2) var(--moox-space-3);
  color: var(--color-text-2);
  background: var(--color-fill-2);
  border-radius: 4px;
  font-size: 13px;
}

.estimate.is-empty {
  color: var(--color-text-3);
}
</style>
