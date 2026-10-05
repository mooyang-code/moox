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

    <a-alert v-if="backfill" type="success" show-icon closable class="member-section__notice" @close="backfill = null">
      因子 {{ backfill.factorId }} 已启用，历史回填任务 {{ backfill.jobId }} 已创建。
      <a-link @click="openBackfill">查看回填</a-link>
    </a-alert>

    <a-empty v-if="!props.info.members.length" description="还没有因子。点击「添加因子」，从「因子定义」中选择。" />
    <ul v-else class="member-list">
      <li v-for="row in rows" :key="row.member.factor_id" class="member-row">
        <div class="member-row__main">
          <div class="member-row__name">
            <strong>{{ row.member.factor_id }}</strong>
            <a-tag size="small">{{ factorTypeLabel(row.member.factor.factor_type) }}</a-tag>
          </div>
          <div class="member-row__outputs">输出 {{ row.member.factor.outputs.join(", ") || "-" }}</div>
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
            <a-button size="mini" type="text" status="danger" :loading="pending === row.member.factor_id">
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
                  :loading="pending === row.member.factor_id"
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

    <p class="member-section__hint">启用时加列并自动回填历史；停用保留结果列。已启用的因子需先停用才能编辑定义或移除。</p>
    <p v-if="props.info.last_run?.last_period_time" class="member-section__hint">
      最近周期 {{ formatPeriod(props.info.last_run.last_period_time) }}，滞后 {{ formatLag(props.info.last_run.lag_seconds) }}
    </p>

    <AddFactorModal v-model:visible="addVisible" :info="props.info" :title="props.title" @added="onAdded" />
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { removeFactorFromSet, setFactorMemberStatus } from "@/api/factor";
import type { FactorMember, FactorSetInfo, MemberStatus } from "@/api/factor/types";
import { useFactorStore } from "@/store/modules/factor";
import { formatLag, formatPeriod } from "@/views/factor/shared/health";
import { factorTypeLabel, memberStatusTag, periodStatusTag } from "@/views/factor/shared/status";
import AddFactorModal from "./add-factor-modal.vue";
import { addFactorState, ENABLE_ENGINE_HINT, memberActions, memberCounts, memberPeriodState } from "./compute-tasks-model";

defineOptions({ name: "FactorMemberSection" });

const props = defineProps<{ info: FactorSetInfo; title: string }>();

const router = useRouter();
const store = useFactorStore();
const addVisible = ref(false);
const pending = ref("");
const backfill = ref<{ factorId: string; jobId: string } | null>(null);

const counts = computed(() => memberCounts(props.info));
const addState = computed(() => addFactorState(props.info.factor_set));
const rows = computed(() =>
  props.info.members.map(member => ({
    member,
    actions: memberActions(props.info.factor_set, member),
    period: memberPeriodState(props.info, member.factor_id)
  }))
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

async function toggle(member: FactorMember, target: MemberStatus) {
  pending.value = member.factor_id;
  try {
    const result = await setFactorMemberStatus(member.set_id, member.factor_id, target);
    if (target === "enabled") {
      backfill.value = result.backfill_job ? { factorId: member.factor_id, jobId: result.backfill_job.job_id } : null;
      Message.success(
        result.backfill_job ? `因子 ${member.factor_id} 已启用，历史回填已开始` : `因子 ${member.factor_id} 已启用`
      );
    } else {
      Message.success(`因子 ${member.factor_id} 已停用`);
    }
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "更新因子状态失败");
  } finally {
    pending.value = "";
  }
}

async function remove(member: FactorMember) {
  try {
    await removeFactorFromSet(member.set_id, member.factor_id);
    Message.success(`因子 ${member.factor_id} 已从计算任务移除，结果列保留`);
    await reload();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "移除因子失败");
  }
}

function openBackfill() {
  if (!backfill.value) return;
  void router.push({
    path: "/factor/tasks",
    query: { tab: "recalc", set: props.info.factor_set.set_id, job: backfill.value.jobId }
  });
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

.member-section__notice {
  margin-bottom: var(--moox-space-2);
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
