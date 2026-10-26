<template>
  <section class="member-section" aria-label="因子成员">
    <div class="member-section__head">
      <h3>因子（启用 {{ counts.enabled }} / 共 {{ counts.total }}）</h3>
      <a-tooltip :content="addState.reason" :disabled="!addState.disabled">
        <span>
          <a-button type="primary" status="success" size="small" :disabled="addState.disabled" @click="addVisible = true">
            <template #icon><icon-plus /></template>
            添加因子
          </a-button>
        </span>
      </a-tooltip>
    </div>

    <a-empty v-if="!props.info.members.length" description="还没有因子。点击「添加因子」，从「因子定义」中选择。" />
    <ul v-else class="member-list">
      <li v-for="row in rows" :key="row.key" class="member-row">
        <div class="member-row__main">
          <div class="member-row__name">
            <strong>{{ row.key }}</strong>
            <span v-if="row.key !== row.member.factor_id" class="member-row__definition">定义 {{ row.member.factor_id }}</span>
            <a-tag size="small">{{ factorTypeLabel(row.member.factor.factor_type) }}</a-tag>
            <a-tag size="small" :color="row.member.kind === 'var' ? 'purple' : 'gray'">
              {{ row.member.kind === "var" ? "可变" : "固定" }}
            </a-tag>
          </div>
          <div class="member-row__outputs">结果列 {{ row.outputs.join(", ") || "-" }}</div>
          <div v-if="row.paramsText" class="member-row__outputs">
            {{ row.member.kind === "var" ? "默认参数" : "固定参数" }} {{ row.paramsText }}
          </div>
          <div v-if="row.member.kind === 'var'" class="member-row__outputs">{{ row.submissionText }}</div>
        </div>
        <a-tag size="small" :color="memberStatusTag(row.member.status).color">{{
          memberStatusTag(row.member.status).label
        }}</a-tag>
        <a-tag v-if="row.period" size="small" :color="periodStatusTag(row.period.status).color">
          {{ periodStatusTag(row.period.status).label }}
        </a-tag>
        <span v-else class="member-row__period-empty">-</span>
        <a-space size="mini" class="member-row__actions">
          <a-tooltip :content="row.actions.edit.reason" :disabled="!row.actions.edit.disabled">
            <span>
              <a-button size="mini" type="text" :disabled="row.actions.edit.disabled" @click="editDefinition(row.member)">
                编辑定义
              </a-button>
            </span>
          </a-tooltip>
          <a-popconfirm
            v-if="row.actions.toggleTarget === 'disabled'"
            content="停用后不再计算该因子，已写入的结果列保留。确认停用？"
            @ok="toggle(row.member, row.actions.toggleTarget)"
          >
            <a-button size="mini" type="text" status="danger" :loading="pending === row.key">
              {{ row.actions.toggleLabel }}
            </a-button>
          </a-popconfirm>
          <a-tooltip v-else :content="row.actions.toggle.reason" :disabled="!row.actions.toggle.disabled">
            <span>
              <a-popconfirm
                :content="ENABLE_ENGINE_HINT"
                :disabled="row.actions.toggle.disabled"
                @ok="toggle(row.member, row.actions.toggleTarget)"
              >
                <a-button
                  size="mini"
                  type="text"
                  status="success"
                  :disabled="row.actions.toggle.disabled"
                  :loading="pending === row.key"
                >
                  {{ row.actions.toggleLabel }}
                </a-button>
              </a-popconfirm>
            </span>
          </a-tooltip>
          <a-tooltip :content="row.actions.remove.reason" :disabled="!row.actions.remove.disabled">
            <span>
              <a-popconfirm content="移除后该因子不再属于这个计算任务（已写入的结果列保留）。确认移除？" @ok="remove(row.member)">
                <a-button size="mini" type="text" status="danger" :disabled="row.actions.remove.disabled">移除</a-button>
              </a-popconfirm>
            </span>
          </a-tooltip>
        </a-space>
      </li>
    </ul>

    <p class="member-section__hint">
      启用时加列，不自动补算历史（需要时到「补算」手动提交）；停用保留结果列。已启用的因子需先停用才能编辑定义或移除。固定参数（可变成员的默认参数）加成员之后不再改，要换参数请另加一个成员。
    </p>
    <p v-if="props.info.last_run?.last_period_time" class="member-section__hint">
      最近周期 {{ formatPeriod(props.info.last_run.last_period_time) }}，{{
        barLagText(props.info.factor_task.freq, props.info.last_run)
      }}
    </p>

    <AddFactorModal v-model:visible="addVisible" :info="props.info" :title="props.title" @added="onAdded" />
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { removeFactorFromTask, setFactorMemberStatus } from "@/api/factor";
import type { FactorMember, FactorTaskInfo, MemberStatus } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { barLagText, formatPeriod } from "@/views/factor/shared/health";
import { paramsText } from "@/views/factor/shared/params";
import { factorTypeLabel, memberStatusTag, periodStatusTag } from "@/views/factor/shared/status";
import AddFactorModal from "./add-factor-modal.vue";
import {
  addFactorState,
  ENABLE_ENGINE_HINT,
  memberActions,
  lastSubmission,
  memberCounts,
  memberPeriodState
} from "./compute-tasks-model";

defineOptions({ name: "FactorMemberSection" });

const props = defineProps<{ info: FactorTaskInfo; title: string }>();

const router = useRouter();
const store = useFactorStore();
const addVisible = ref(false);
const pending = ref("");

const counts = computed(() => memberCounts(props.info));
const addState = computed(() => addFactorState(props.info.factor_task));
const rows = computed(() =>
  props.info.members.map(member => {
    const key = member.member_key || member.factor_id;
    const period = memberPeriodState(props.info, key);
    const submitted = lastSubmission(member);
    return {
      member,
      key,
      actions: memberActions(props.info.factor_task, member),
      period,
      outputs: member.output_columns?.length ? member.output_columns : member.factor.outputs,
      paramsText: paramsText(member.params),
      submissionText: submitted
        ? `最近提交：目标期 ${formatPeriod(submitted.period)}，提交于 ${formatTime(submitted.at)}`
        : "尚未提交（按默认参数计算）"
    };
  })
);

function reload() {
  return store.reload();
}

function onAdded() {
  void reload();
}

function editDefinition(member: FactorMember) {
  void router.push(`/factor/definitions/${encodeURIComponent(member.factor_id)}/edit`);
}

function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}

async function toggle(member: FactorMember, target: MemberStatus) {
  const key = member.member_key;
  pending.value = key;
  try {
    await setFactorMemberStatus(member.task_id, key, target);
    Message.success(
      target === "enabled" ? `因子 ${key} 已启用（不自动补算历史，需要时到「补算」手动提交）` : `因子 ${key} 已停用`
    );
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "更新因子状态失败");
  } finally {
    pending.value = "";
  }
}

async function remove(member: FactorMember) {
  try {
    const key = member.member_key;
    await removeFactorFromTask(member.task_id, key);
    Message.success(`因子 ${key} 已从计算任务移除，结果列保留`);
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "移除因子失败");
  }
}
</script>

<style scoped>
.member-section__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.member-section__head h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
}

.member-list {
  margin: 0;
  padding: 0;
  list-style: none;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
}

.member-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-3);
  padding: 10px 12px;
  border-bottom: 1px solid var(--color-border-2);
}

.member-row:last-child {
  border-bottom: 0;
}

.member-row__main {
  min-width: 160px;
  flex: 1;
}

.member-row__name {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
}

.member-row__definition,
.member-row__outputs,
.member-row__period-empty,
.member-section__hint {
  color: var(--color-text-3);
  font-size: 12px;
}

.member-section__hint {
  margin: var(--moox-space-2) 0 0;
}
</style>
