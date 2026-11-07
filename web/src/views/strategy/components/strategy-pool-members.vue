<template>
  <div class="pool-members">
    <table class="pool-table" aria-label="策略池当期成员明细">
      <thead>
        <tr>
          <th>成员</th>
          <th>状态</th>
          <th class="num">净值</th>
          <th class="num">基础比例</th>
          <th v-if="showTiming">择时</th>
          <th class="num">步长前比例</th>
          <th class="num">实际比例</th>
          <th v-if="hasRank" class="num">综合名次分</th>
          <th class="num">总敞口</th>
          <th class="num">净敞口</th>
          <th></th>
        </tr>
      </thead>
      <tbody>
        <template v-for="member in period.members" :key="member.id">
          <tr class="member-row">
            <td>
              <span class="swatch" :style="{ background: colorOf(member.id) }" aria-hidden="true" />
              <span class="mono member-id">{{ member.id }}</span>
            </td>
            <td>
              <template v-if="member.status === 'ok'">正常</template>
              <div v-else class="status-cell">
                <span class="skipped" :title="member.detail || undefined">没通过：{{ skipReasonLabel(member.reason) }}</span>
                <span class="muted">沿用上一期目标权重</span>
              </div>
            </td>
            <td class="num">
              {{ member.equity === null ? "-" : member.equity.toFixed(4) }}
              <span v-if="member.bankrupt" class="bankrupt">爆仓</span>
            </td>
            <td class="num">{{ pct(member.base) }}</td>
            <td v-if="showTiming">{{ member.timing === null ? "-" : member.timing === 1 ? "持有" : "空仓" }}</td>
            <td class="num">{{ pct(member.ratioBefore) }}</td>
            <td class="num strong">{{ pct(member.ratio) }}</td>
            <td v-if="hasRank" class="num">{{ member.rank === null ? "-" : String(Number(member.rank.toFixed(4))) }}</td>
            <td class="num">{{ pct(member.gross) }}</td>
            <td class="num">{{ pct(member.net) }}</td>
            <td class="toggle-cell">
              <button
                type="button"
                class="toggle"
                :aria-expanded="isOpen(member.id)"
                :aria-label="`${isOpen(member.id) ? '收起' : '展开'}成员 ${member.id} 的贡献权重`"
                @click="toggle(member.id)"
              >
                {{ isOpen(member.id) ? "收起" : "贡献权重" }}
              </button>
            </td>
          </tr>
          <tr v-if="isOpen(member.id)" class="targets-row">
            <td :colspan="columnCount">
              <div v-if="!member.targets.length" class="muted">本期该成员没有贡献权重</div>
              <table v-else class="targets-table" :aria-label="`成员 ${member.id} 的贡献权重`">
                <thead>
                  <tr>
                    <th>标的</th>
                    <th>市场</th>
                    <th class="num">贡献权重</th>
                    <th>方向</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="target in member.targets" :key="target.key">
                    <td class="mono">{{ target.instrumentId }}</td>
                    <td>{{ marketLabel(target.market) }}</td>
                    <td :class="['num', target.weight < 0 ? 'short' : 'long']">{{ pct(target.weight) }}</td>
                    <td>{{ target.weight < 0 ? "空" : "多" }}</td>
                  </tr>
                </tbody>
              </table>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { marketLabel } from "@/views/strategy/market-mode";
import { percent, skipReasonLabel } from "@/views/strategy/model";
import { memberColor, type PoolPeriod } from "@/views/strategy/pool-model";

// 某一期的策略池成员明细：每个成员一行，行可以展开看它对各（标的|市场）的贡献权重。
const props = defineProps<{
  period: PoolPeriod;
  /** 全部成员 id（决定颜色顺序，与图表一致）。 */
  ids: string[];
  showTiming: boolean;
}>();

const expanded = ref<Set<string>>(new Set());
const hasRank = computed(() => props.period.members.some(member => member.rank !== null));
// 成员、状态、净值、基础比例、步长前、实际、总敞口、净敞口、操作，再加可选的择时与名次分。
const columnCount = computed(() => 9 + (props.showTiming ? 1 : 0) + (hasRank.value ? 1 : 0));

function colorOf(id: string): string {
  return memberColor(Math.max(0, props.ids.indexOf(id)));
}
function pct(value: number | null): string {
  return value === null ? "-" : percent(value);
}
function isOpen(id: string): boolean {
  return expanded.value.has(id);
}
function toggle(id: string) {
  const next = new Set(expanded.value);
  if (!next.delete(id)) next.add(id);
  expanded.value = next;
}
</script>

<style scoped>
.pool-members {
  overflow-x: auto;
}
.pool-table,
.targets-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.pool-table th,
.pool-table td {
  padding: 6px 10px;
  border-bottom: 1px solid var(--color-border-2);
  text-align: left;
  white-space: nowrap;
}
.pool-table th {
  color: var(--color-text-3);
  font-weight: 400;
}
.num {
  text-align: right !important;
  font-variant-numeric: tabular-nums;
}
.strong {
  font-weight: 600;
}
.swatch {
  display: inline-block;
  width: 10px;
  height: 10px;
  margin-right: 8px;
  border-radius: 2px;
  vertical-align: baseline;
}
.mono {
  font-family: var(--font-family-mono, ui-monospace, monospace);
}
.status-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.skipped {
  color: rgb(var(--orange-6));
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.bankrupt {
  margin-left: 6px;
  color: rgb(var(--red-6));
}
.toggle {
  padding: 0;
  border: 0;
  background: none;
  color: rgb(var(--arcoblue-6));
  font-size: 12px;
  cursor: pointer;
}
.targets-row td {
  padding: 8px 10px 12px 28px;
  background: var(--color-fill-1);
}
.targets-table {
  max-width: 560px;
}
.targets-table th,
.targets-table td {
  padding: 3px 10px;
  text-align: left;
  white-space: nowrap;
}
.targets-table th {
  color: var(--color-text-3);
  font-weight: 400;
}
.long {
  color: rgb(var(--green-6));
}
.short {
  color: rgb(var(--red-6));
}
</style>
