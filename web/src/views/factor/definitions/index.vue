<template>
  <div class="moox-page factor-definitions-page">
    <div class="moox-inner">
      <div class="page-head definitions-head">
        <div class="page-head__title">
          <h2>因子定义</h2>
          <InfoTip
            text="因子定义只描述算法本身：代码、参数、输入输出列。它不属于任何计算任务；要让因子运行，请到「计算任务」里添加并启用。"
          />
        </div>
      </div>

      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <a-alert v-if="loadError" type="error" show-icon class="definitions-alert">
          {{ loadError }}
          <template #action><a-button size="mini" @click="load">重试</a-button></template>
        </a-alert>

        <a-space class="task-toolbar" wrap>
          <a-button type="primary" status="success" @click="createFactor">
            <template #icon><icon-plus /></template>
            新增因子
          </a-button>
          <a-input
            v-model="filters.keyword"
            placeholder="按因子 ID 或模块名筛选"
            allow-clear
            style="width: 200px"
            @press-enter="search"
          />
          <a-select v-model="filters.type" placeholder="因子类型" style="width: 120px">
            <a-option value="">全部类型</a-option>
            <a-option value="timeseries">时序</a-option>
            <a-option value="cross_section">横截面</a-option>
          </a-select>
          <a-select v-model="filters.usage" placeholder="使用情况" style="width: 140px">
            <a-option value="all">全部（{{ counts.all }}）</a-option>
            <a-option value="using">使用中（{{ counts.using }}）</a-option>
            <a-option value="idle">未使用（{{ counts.idle }}）</a-option>
          </a-select>
          <a-button type="primary" @click="search">
            <template #icon><icon-search /></template>
            查询
          </a-button>
        </a-space>

        <a-table
          row-key="factor_id"
          size="small"
          :bordered="{ cell: true }"
          :data="rows"
          :loading="loading"
          :pagination="{ pageSize: 20, hideOnSinglePage: true }"
          :scroll="{ x: 'max-content' }"
        >
          <template #empty>
            <a-empty description="没有符合条件的因子定义" />
          </template>
          <template #columns>
            <a-table-column title="因子 ID" :width="190">
              <template #cell="{ record }">
                <a-link @click="detailId = record.factor.factor_id">{{ record.factor.factor_id }}</a-link>
                <div class="cell-sub">模块 {{ record.factor.name }}</div>
              </template>
            </a-table-column>
            <a-table-column title="类型" :width="100">
              <template #cell="{ record }">{{ factorTypeLabel(record.factor.factor_type) }}</template>
            </a-table-column>
            <a-table-column title="输入列" :width="180" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ record.factor.input_columns.join(", ") }}</template>
            </a-table-column>
            <a-table-column title="输出列" :width="180" :ellipsis="true" :tooltip="true">
              <template #cell="{ record }">{{ record.factor.outputs.join(", ") }}</template>
            </a-table-column>
            <a-table-column title="回看周期" data-index="factor.lookback_periods" :width="90" />
            <a-table-column title="使用情况" :width="300">
              <template #cell="{ record }">
                <span v-if="!record.chips.length" class="usage-empty">未被使用</span>
                <a-space v-else wrap size="mini">
                  <button
                    v-for="chip in record.chips"
                    :key="chip.setId"
                    type="button"
                    class="usage-chip"
                    :title="`${chip.label}（${chip.statusLabel}）`"
                    @click="openTask(chip.setId)"
                  >
                    <span class="usage-dot" :class="`usage-dot--${chip.status}`"></span>
                    {{ chip.label }}
                  </button>
                </a-space>
              </template>
            </a-table-column>
            <a-table-column title="操作" :width="200" align="center" fixed="right">
              <template #cell="{ record }">
                <a-space>
                  <a-button size="mini" type="text" @click="detailId = record.factor.factor_id">详情</a-button>
                  <a-tooltip :content="record.edit.reason" :disabled="!record.edit.disabled">
                    <span>
                      <a-button
                        size="mini"
                        type="text"
                        :disabled="record.edit.disabled"
                        @click="editFactor(record.factor.factor_id)"
                      >
                        编辑
                      </a-button>
                    </span>
                  </a-tooltip>
                  <a-tooltip :content="record.remove.reason" :disabled="!record.remove.disabled">
                    <span>
                      <a-popconfirm content="删除后不可恢复，确认继续？" @ok="removeFactor(record.factor.factor_id)">
                        <a-button size="mini" type="text" status="danger" :disabled="record.remove.disabled">删除</a-button>
                      </a-popconfirm>
                    </span>
                  </a-tooltip>
                </a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </template>
    </div>

    <FactorDetailDrawer :factor-id="detailId" @close="detailId = ''" @open-task="openTask" />
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onMounted, reactive, ref } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { deleteFactor, listFactors } from "@/api/factor";
import type { FactorInfo, FactorSetInfo } from "@/api/factor/types";
import { RequestGate } from "@/utils/request-gate";
import InfoTip from "@/views/factor/shared/info-tip.vue";
import { factorTypeLabel } from "@/views/factor/shared/status";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import {
  definitionDeleteState,
  definitionEditState,
  filterDefinitions,
  usageChips,
  usageCounts,
  type DefinitionFilters
} from "./definitions-model";
import FactorDetailDrawer from "./factor-detail-drawer.vue";

defineOptions({ name: "FactorDefinitions" });

const PAGE_SIZE = 500;

const router = useRouter();
const { store, spaceId } = useFactorScope({ poll: false });

// filters 是表单里的草稿，applied 是点「查询」后生效的条件，与采集任务页一致。
const filters = reactive<DefinitionFilters>({ type: "", usage: "all", keyword: "" });
const applied = reactive<DefinitionFilters>({ ...filters });
const items = ref<FactorInfo[]>([]);
const loading = ref(false);
const loadError = ref("");
const detailId = ref("");
const gate = new RequestGate();

const counts = computed(() => usageCounts(items.value));

function setLabelOf(setId: string) {
  const info = store.sets.find((item: FactorSetInfo) => item.factor_set.set_id === setId);
  return info ? store.setLabel(info.factor_set) : setId;
}

const rows = computed(() =>
  filterDefinitions(items.value, applied).map(item => ({
    factor: item.factor,
    chips: usageChips(item.usages, setLabelOf),
    edit: definitionEditState(item),
    remove: definitionDeleteState(item)
  }))
);

function search() {
  Object.assign(applied, filters);
  void load();
}

/** 列表默认不带源码（D24）；详情抽屉再按需 GetFactor。 */
async function load() {
  const token = gate.next();
  loading.value = true;
  try {
    const all: FactorInfo[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactors({ page: { page, size: PAGE_SIZE } });
      const batch = rsp.factors || [];
      all.push(...batch);
      if (!rsp.page_result?.has_more || !batch.length) break;
    }
    if (!gate.isCurrent(token)) return;
    items.value = all;
    loadError.value = "";
  } catch (error) {
    if (gate.isCurrent(token)) loadError.value = error instanceof Error ? error.message : "因子定义加载失败";
  } finally {
    if (gate.isCurrent(token)) loading.value = false;
  }
}

function createFactor() {
  void router.push("/factor/definitions/new");
}

function editFactor(factorId: string) {
  void router.push(`/factor/definitions/${encodeURIComponent(factorId)}/edit`);
}

function openTask(setId: string) {
  void router.push({ path: "/factor/tasks", query: { detail: setId } });
}

async function removeFactor(factorId: string) {
  try {
    await deleteFactor(factorId);
    Message.success("因子已删除");
    await load();
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "删除因子失败");
  }
}

// 成员在计算任务里被添加 / 移除 / 启停后，使用情况会变化；切回本页（keep-alive 激活）时重新拉取。
let fresh = false;
onMounted(() => {
  fresh = true;
  void load();
});
onActivated(() => {
  if (fresh) {
    fresh = false;
    return;
  }
  void load();
  void store.reload();
});
</script>

<style scoped lang="scss">
@use "../shared/factor-page.scss";

.definitions-head {
  margin-bottom: var(--moox-space-3);
}

.definitions-alert {
  margin-bottom: var(--moox-space-3);
}

.task-toolbar {
  margin-bottom: var(--moox-space-toolbar-table);
}

.cell-sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.usage-empty {
  color: var(--color-text-3);
}

.usage-chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 1px 8px;
  color: var(--color-text-2);
  font-size: 12px;
  background: var(--color-fill-2);
  border: 1px solid var(--color-border-2);
  border-radius: 10px;
  cursor: pointer;
}

.usage-chip:hover {
  border-color: rgb(var(--primary-6));
}

.usage-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--color-neutral-5);
}

.usage-dot--enabled {
  background: rgb(var(--success-6));
}
</style>
