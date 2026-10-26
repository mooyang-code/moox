<template>
  <section v-if="binding" class="factor-params" aria-label="因子参数">
    <a-descriptions :column="{ xs: 1, md: 2 }" bordered size="small">
      <a-descriptions-item label="因子任务">
        <a-link @click="openResults"
          ><span class="mono">{{ binding.taskId }}</span></a-link
        >
      </a-descriptions-item>
      <a-descriptions-item label="可变成员">{{ binding.members.join("、") || "无（任务里没有可变成员）" }}</a-descriptions-item>
      <a-descriptions-item label="结果 View" :span="2"
        ><span class="mono">{{ binding.viewId }}</span></a-descriptions-item
      >
    </a-descriptions>

    <a-alert v-if="error" type="error" show-icon class="factor-params__alert">
      {{ error }}<template #action><a-button size="mini" @click="load">重试</a-button></template>
    </a-alert>

    <h4>因子侧最近一次接受的提交</h4>
    <p class="hint">
      每期开算前，因子引擎读取每个可变成员最近一次被接受的提交；从未提交过的成员用定义的默认参数。每期实际用的参数随结果记在结果数据集的
      __params 列里。
    </p>
    <a-table row-key="member_key" size="small" :data="current" :loading="loading" :pagination="false">
      <template #empty><a-empty description="任务里没有可变成员，或还没有提交" /></template>
      <template #columns>
        <a-table-column title="成员" data-index="member_key" :width="150" />
        <a-table-column title="目标期" :width="190">
          <template #cell="{ record }">{{ periodText(record.last_submitted_period) }}</template>
        </a-table-column>
        <a-table-column title="参数" data-index="summary" :ellipsis="true" :tooltip="true" />
        <a-table-column title="提交时间" :width="190">
          <template #cell="{ record }">{{
            record.last_submitted_at ? new Date(record.last_submitted_at).toLocaleString() : "-"
          }}</template>
        </a-table-column>
      </template>
    </a-table>

    <h4>策略每期产出的下一期参数</h4>
    <p class="hint">
      参数随结果一起落库，再异步送达因子管理端；只投当前会话里最新的一条，被明确拒绝的不再重试。「已送达」不等于「已生效」：只有在下一期开算之前到达才会被采用。
    </p>
    <a-table
      row-key="resultId"
      size="small"
      :data="produced"
      :loading="loading"
      :pagination="{ pageSize: 10, hideOnSinglePage: true }"
    >
      <template #empty><a-empty description="还没有产出参数的结果（策略没有声明 factor_params，或还没有 ok 的周期）" /></template>
      <template #columns>
        <a-table-column title="结果周期" data-index="barEnd" :width="190" />
        <a-table-column title="目标期" data-index="targetText" :width="190" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tooltip :content="record.hint"
              ><a-tag size="small" :color="record.statusColor">{{ record.statusLabel }}</a-tag></a-tooltip
            >
          </template>
        </a-table-column>
        <a-table-column title="参数" data-index="summary" :ellipsis="true" :tooltip="true" />
      </template>
    </a-table>

    <h4>外显设置</h4>
    <p class="hint">
      选择在结果表里隐藏的内容，默认全部展示。只影响控制台展示与默认导出，不影响策略能读到的数据；参数列跟随所属因子显示或隐藏。
    </p>
    <a-form layout="vertical" :model="hidden" class="display-form">
      <a-form-item label="隐藏分类">
        <a-checkbox-group v-model="hidden.categories">
          <a-checkbox v-for="category in categories" :key="category.category_id" :value="category.category_id">
            {{ category.name }}
          </a-checkbox>
        </a-checkbox-group>
      </a-form-item>
      <a-form-item label="隐藏成员">
        <a-checkbox-group v-model="hidden.members">
          <a-checkbox v-for="member in binding.members" :key="member" :value="member">{{ member }}</a-checkbox>
        </a-checkbox-group>
      </a-form-item>
      <a-form-item label="隐藏列">
        <a-select
          v-model="hidden.columns"
          multiple
          allow-clear
          allow-search
          placeholder="按列名选择要隐藏的列"
          :max-tag-count="6"
        >
          <a-option v-for="column in columnOptions" :key="column" :value="column">{{ column }}</a-option>
        </a-select>
      </a-form-item>
      <a-space>
        <a-button type="primary" :loading="saving" @click="saveDisplay">保存外显设置</a-button>
        <a-button :disabled="!hasHidden" @click="clearHidden">全部展示</a-button>
      </a-space>
    </a-form>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { getFactorParams, listFactorCategories } from "@/api/factor";
import type { FactorCategory, MemberParamState } from "@/api/factor/types";
import { listAllViewColumns } from "@/api/storage/metadata";
import { getStrategyDisplaySettings, listStrategyResults, setStrategyDisplaySettings } from "@/api/strategy";
import type { DisplayHidden, StrategyInstance, StrategyResult } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { memberParamsSummary, nextParamsRows, parseFactorBinding, periodText } from "@/views/strategy/factor-params-model";

defineOptions({ name: "StrategyFactorParams" });

const props = defineProps<{ instance: StrategyInstance }>();

const router = useRouter();
const spaceStore = useSpaceStore();
const binding = computed(() => parseFactorBinding(props.instance.resolved_json));

const loading = ref(false);
const saving = ref(false);
const error = ref("");
const members = ref<MemberParamState[]>([]);
const results = ref<StrategyResult[]>([]);
const categories = ref<FactorCategory[]>([]);
const columnOptions = ref<string[]>([]);
const hidden = reactive<DisplayHidden>({ categories: [], members: [], columns: [] });

/** 因子侧每个可变成员最近一次被接受的提交，附上参数摘要。 */
const current = computed(() =>
  members.value.map(item => ({
    ...item,
    summary: memberParamsSummary({ [item.member_key]: item.params ?? {} }).replace(/^[^:]*: /, "")
  }))
);
const produced = computed(() => nextParamsRows(results.value));
const hasHidden = computed(() => hidden.categories.length + hidden.members.length + hidden.columns.length > 0);

async function load() {
  const taskBinding = binding.value;
  if (!taskBinding) return;
  loading.value = true;
  error.value = "";
  try {
    const [params, list, settings, cats] = await Promise.all([
      getFactorParams({ task_id: taskBinding.taskId }),
      listStrategyResults(props.instance.instance_id, { page_size: 100, session_id: props.instance.session_id || undefined }),
      getStrategyDisplaySettings(props.instance.instance_id),
      listFactorCategories().catch(() => [] as FactorCategory[])
    ]);
    members.value = params.members ?? [];
    results.value = list.items;
    categories.value = cats;
    Object.assign(hidden, {
      categories: [...settings.categories],
      members: [...settings.members],
      columns: [...settings.columns]
    });
    await loadColumns(taskBinding.viewId);
  } catch (e) {
    error.value = e instanceof Error ? e.message : "因子参数加载失败";
  } finally {
    loading.value = false;
  }
}

/** 列名候选取自私有结果 View；读取失败不影响其余内容。 */
async function loadColumns(viewId: string) {
  const space = spaceStore.selectedSpaceId;
  if (!space || !viewId) return;
  try {
    const columns = await listAllViewColumns(space, viewId);
    columnOptions.value = columns.map(column => column.column_name).sort();
  } catch {
    columnOptions.value = [];
  }
}

async function saveDisplay() {
  saving.value = true;
  try {
    const saved = await setStrategyDisplaySettings(props.instance.instance_id, {
      categories: [...hidden.categories],
      members: [...hidden.members],
      columns: [...hidden.columns]
    });
    Object.assign(hidden, saved);
    Message.success("外显设置已保存");
  } catch (e) {
    Message.error(e instanceof Error ? e.message : "保存外显设置失败");
  } finally {
    saving.value = false;
  }
}

function clearHidden() {
  Object.assign(hidden, { categories: [], members: [], columns: [] });
}

function openResults() {
  if (binding.value) void router.push({ path: "/factor/results", query: { task: binding.value.taskId } });
}

// 多源 watch 按每个源逐个比较：父页面每 10 秒整体替换实例对象时，实例与会话没变就不重新加载，
// 否则会把用户还没保存的外显设置覆盖掉（单个返回新数组的 getter 每次都会判为变化）。
watch([() => props.instance.instance_id, () => props.instance.session_id], () => void load(), { immediate: true });
</script>

<style scoped>
.factor-params h4 {
  margin: 18px 0 4px;
  font-size: 14px;
}

.factor-params__alert {
  margin-top: 12px;
}

.hint {
  margin: 0 0 8px;
  color: var(--color-text-3);
  font-size: 12px;
}

.display-form {
  max-width: 720px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
