<template>
  <section v-if="binding" class="factor-params" aria-label="因子参数">
    <a-descriptions :column="{ xs: 1, md: 2 }" bordered size="small">
      <a-descriptions-item label="私有因子集">
        <a-link @click="openResults"
          ><span class="mono">{{ binding.setId }}</span></a-link
        >
      </a-descriptions-item>
      <a-descriptions-item label="成员（因子别名）">{{ binding.members.join("、") || "-" }}</a-descriptions-item>
      <a-descriptions-item label="结果 View"
        ><span class="mono">{{ binding.viewId }}</span></a-descriptions-item
      >
      <a-descriptions-item label="冻结到">{{ frozenThrough }}</a-descriptions-item>
    </a-descriptions>

    <a-alert v-if="error" type="error" show-icon class="factor-params__alert">
      {{ error }}<template #action><a-button size="mini" @click="load">重试</a-button></template>
    </a-alert>

    <h4>实际生效的参数（冻结记录）</h4>
    <p class="hint">
      每期开算前由因子管理端冻结：用该期最后一份被接受的提交，没有就沿用上一期，从未提交过用默认值。以下按区间合并显示（快照与来源相同的连续期次为一行）。
    </p>
    <a-table
      row-key="key"
      size="small"
      :data="timeline"
      :loading="loading"
      :pagination="{ pageSize: 10, hideOnSinglePage: true }"
    >
      <template #empty><a-empty description="还没有冻结记录（等待第一个周期）" /></template>
      <template #columns>
        <a-table-column title="成员" data-index="member" :width="110" />
        <a-table-column title="起始期" :width="190">
          <template #cell="{ record }">{{ freezePeriodText(record.start) }}</template>
        </a-table-column>
        <a-table-column title="期数" data-index="periods" :width="70" />
        <a-table-column title="来源" :width="100">
          <template #cell="{ record }"
            ><a-tag size="small">{{ record.sourceText }}</a-tag></template
          >
        </a-table-column>
        <a-table-column title="参数" data-index="params" :ellipsis="true" :tooltip="true" />
      </template>
    </a-table>

    <h4>策略的提交记录</h4>
    <p class="hint">策略每期求出下一期的参数并提交；「已接受」不等于「已生效」，某期是否采用以上面的冻结记录为准。</p>
    <a-table
      row-key="request_id"
      size="small"
      :data="submissions"
      :loading="loading"
      :pagination="{ pageSize: 10, hideOnSinglePage: true }"
    >
      <template #empty><a-empty description="还没有提交记录（策略没有声明 factor_params，或还没有 ok 的周期）" /></template>
      <template #columns>
        <a-table-column title="目标期次" :width="190" data-index="periodText" />
        <a-table-column title="状态" :width="90">
          <template #cell="{ record }">
            <a-tooltip :content="record.hint"
              ><a-tag size="small" :color="record.statusColor">{{ record.statusLabel }}</a-tag></a-tooltip
            >
          </template>
        </a-table-column>
        <a-table-column title="参数" data-index="summary" :ellipsis="true" :tooltip="true" />
        <a-table-column title="尝试" data-index="attempts" :width="70" />
        <a-table-column title="说明" :ellipsis="true" :tooltip="true">
          <template #cell="{ record }">{{ [record.outcome, record.notes].filter(Boolean).join("；") || "-" }}</template>
        </a-table-column>
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
import type { FactorCategory, ParamInterval } from "@/api/factor/types";
import { listViewColumns } from "@/api/storage/metadata";
import { getStrategyDisplaySettings, listStrategyParamSubmissions, setStrategyDisplaySettings } from "@/api/strategy";
import type { DisplayHidden, ParamSubmission, StrategyInstance } from "@/api/strategy-types";
import { useSpaceStore } from "@/store/modules/space";
import { freezePeriodText, parseFactorBinding, submissionRows } from "@/views/strategy/factor-params-model";
import { paramTimeline } from "@/views/factor/shared/params";

defineOptions({ name: "StrategyFactorParams" });

const props = defineProps<{ instance: StrategyInstance }>();

const router = useRouter();
const spaceStore = useSpaceStore();
const binding = computed(() => parseFactorBinding(props.instance.resolved_json));

const loading = ref(false);
const saving = ref(false);
const error = ref("");
const intervals = ref<ParamInterval[]>([]);
const frozenSeconds = ref<number | string>(0);
const rawSubmissions = ref<ParamSubmission[]>([]);
const categories = ref<FactorCategory[]>([]);
const columnOptions = ref<string[]>([]);
const hidden = reactive<DisplayHidden>({ categories: [], members: [], columns: [] });

const WINDOW_SECONDS = 7 * 24 * 3600;
const timeline = computed(() => paramTimeline(intervals.value, periodSeconds()));
const submissions = computed(() => submissionRows(rawSubmissions.value));
const frozenThrough = computed(() => (Number(frozenSeconds.value) > 0 ? freezePeriodText(frozenSeconds.value) : "还没有冻结"));
const hasHidden = computed(() => hidden.categories.length + hidden.members.length + hidden.columns.length > 0);

/** 估算一个周期的秒数，用来给冻结区间标注期数；resolved 里的 bar 如 1m、1h、1d。 */
function periodSeconds() {
  const match = /^(\d+)([smhd])$/.exec(String(parsedBar()));
  if (!match) return 0;
  return Number(match[1]) * { s: 1, m: 60, h: 3600, d: 86400 }[match[2] as "s" | "m" | "h" | "d"];
}

function parsedBar() {
  try {
    return JSON.parse(props.instance.resolved_json || "{}").bar ?? "";
  } catch {
    return "";
  }
}

async function load() {
  const current = binding.value;
  if (!current) return;
  loading.value = true;
  error.value = "";
  try {
    const now = Math.floor(Date.now() / 1000);
    const [params, list, settings, cats] = await Promise.all([
      getFactorParams({ set_id: current.setId, from: now - WINDOW_SECONDS }),
      listStrategyParamSubmissions(props.instance.instance_id, 100, { silent: true }),
      getStrategyDisplaySettings(props.instance.instance_id),
      listFactorCategories().catch(() => [] as FactorCategory[])
    ]);
    intervals.value = params.intervals ?? [];
    frozenSeconds.value = params.frozen_through ?? 0;
    rawSubmissions.value = list;
    categories.value = cats;
    Object.assign(hidden, {
      categories: [...settings.categories],
      members: [...settings.members],
      columns: [...settings.columns]
    });
    await loadColumns(current.viewId);
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
    const rsp = await listViewColumns({ space_id: space, view_id: viewId, page: { page: 1, size: 1000 } });
    columnOptions.value = (rsp.columns ?? []).map(column => column.column_name).sort();
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
  if (binding.value) void router.push({ path: "/factor/results", query: { set: binding.value.setId } });
}

watch(
  () => [props.instance.instance_id, props.instance.session_id],
  () => void load(),
  { immediate: true }
);
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
