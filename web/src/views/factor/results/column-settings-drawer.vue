<template>
  <a-drawer :visible="props.visible" title="列设置" :width="460" :footer="false" @cancel="close" @close="close">
    <section class="settings-section">
      <h4>系统列</h4>
      <a-space wrap>
        <a-checkbox
          v-for="key in SYSTEM_KEYS"
          :key="key"
          :model-value="!props.settings.hiddenSystem.includes(key)"
          :disabled="LOCKED_SYSTEM_KEYS.includes(key)"
          @change="toggleSystem(key, $event)"
          >{{ SYSTEM_KEY_LABELS[key] }}</a-checkbox
        >
      </a-space>
    </section>

    <section class="settings-section">
      <h4>行情列</h4>
      <a-space wrap>
        <a-checkbox
          v-for="column in props.scope.baseMarket"
          :key="column"
          :model-value="props.settings.market.includes(column)"
          @change="toggleMarket(column, $event)"
          >{{ marketLabel(column) }}</a-checkbox
        >
      </a-space>
      <template v-if="props.scope.otherSource.length">
        <p class="settings-hint">其他源字段（默认不显示）</p>
        <a-space wrap>
          <a-checkbox
            v-for="column in props.scope.otherSource"
            :key="column"
            :model-value="props.settings.market.includes(column)"
            @change="toggleMarket(column, $event)"
            >{{ marketLabel(column) }}</a-checkbox
          >
        </a-space>
      </template>
    </section>

    <section class="settings-section">
      <h4>
        外显因子列 <span class="settings-hint">{{ props.settings.pinned.length }} / {{ MAX_PINNED }}</span>
      </h4>
      <p v-if="!props.settings.pinned.length" class="settings-hint">还没有钉住的因子列；其余因子值在每行的“查看全部”里看。</p>
      <ul class="pinned-list">
        <li v-for="(column, index) in props.settings.pinned" :key="column">
          <span class="pinned-name" :title="pinnedTitle(column)">{{ column }}</span>
          <a-button size="mini" type="text" :disabled="index === 0" aria-label="上移" @click="move(index, -1)">↑</a-button>
          <a-button
            size="mini"
            type="text"
            :disabled="index === props.settings.pinned.length - 1"
            aria-label="下移"
            @click="move(index, 1)"
            >↓</a-button
          >
          <a-button size="mini" type="text" status="danger" @click="unpin(column)">移除</a-button>
        </li>
      </ul>
      <a-button size="small" type="primary" status="success" :disabled="!props.scope.outputs.length" @click="addPinned">
        <template #icon><icon-plus /></template>添加外显因子列
      </a-button>
    </section>

    <FactorPickerModal
      v-model:visible="pickerVisible"
      mode="pin"
      :scope="props.scope"
      :excluded="props.settings.pinned"
      @pick="pin($event.column)"
    />
  </a-drawer>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { MAX_PINNED, type ColumnSettings } from "./column-settings";
import { LOCKED_SYSTEM_KEYS, MARKET_COLUMN_LABELS, SYSTEM_KEYS, SYSTEM_KEY_LABELS, type FactorScope } from "./factor-scope";
import FactorPickerModal from "./factor-picker-modal.vue";

const props = defineProps<{ visible: boolean; scope: FactorScope; settings: ColumnSettings }>();
const emit = defineEmits<{
  (event: "update:visible", value: boolean): void;
  (event: "update:settings", value: ColumnSettings): void;
}>();

const pickerVisible = ref(false);

function close() {
  emit("update:visible", false);
}

function update(patch: Partial<ColumnSettings>) {
  emit("update:settings", { ...props.settings, ...patch });
}

function checked(value: unknown) {
  return value === true;
}

function toggleSystem(key: string, value: unknown) {
  const hidden = props.settings.hiddenSystem.filter(item => item !== key);
  update({ hiddenSystem: checked(value) ? hidden : [...hidden, key] });
}

/** 勾选的行情列按视图列顺序排列。 */
function toggleMarket(column: string, value: unknown) {
  const shown = new Set(props.settings.market);
  if (checked(value)) shown.add(column);
  else shown.delete(column);
  update({ market: [...props.scope.baseMarket, ...props.scope.otherSource].filter(item => shown.has(item)) });
}

function addPinned() {
  if (props.settings.pinned.length >= MAX_PINNED) {
    Message.warning(`最多钉住 ${MAX_PINNED} 个因子列，更多的因子值请在“查看全部”里看`);
    return;
  }
  pickerVisible.value = true;
}

function pin(column: string) {
  if (!column || props.settings.pinned.includes(column) || props.settings.pinned.length >= MAX_PINNED) return;
  update({ pinned: [...props.settings.pinned, column] });
}

function unpin(column: string) {
  update({ pinned: props.settings.pinned.filter(item => item !== column) });
}

function move(index: number, offset: number) {
  const next = [...props.settings.pinned];
  const target = index + offset;
  if (target < 0 || target >= next.length) return;
  [next[index], next[target]] = [next[target], next[index]];
  update({ pinned: next });
}

function marketLabel(column: string) {
  const label = MARKET_COLUMN_LABELS[column];
  return label ? `${label}（${column}）` : column;
}

function pinnedTitle(column: string) {
  const item = props.scope.outputByColumn.get(column);
  return item ? `${item.memberKey} · ${item.output}` : column;
}
</script>

<style scoped lang="scss">
.settings-section {
  margin-bottom: var(--moox-space-4);

  h4 {
    margin: 0 0 var(--moox-space-2);
    font-weight: 600;
  }
}

.settings-hint {
  margin: var(--moox-space-2) 0;
  color: var(--color-text-3);
  font-size: 12px;
  font-weight: normal;
}

.pinned-list {
  margin: 0 0 var(--moox-space-2);
  padding: 0;
  list-style: none;

  li {
    display: flex;
    align-items: center;
    gap: 4px;
    padding: 2px 0;
  }
}

.pinned-name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
