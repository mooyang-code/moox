<template>
  <div class="moox-page factor-results-workbench">
    <div class="moox-inner">
      <div class="page-head">
        <h2>因子结果</h2>
        <a-space wrap>
          <a-select v-model="selectedSetId" placeholder="选择因子集" :loading="setsLoading" style="width: min(420px, 60vw)" @change="syncRoute">
            <a-option v-for="set in availableSets" :key="set.set_id" :value="set.set_id">
              {{ set.source_dataset_id }} · {{ set.freq }}（{{ set.set_id }}）
            </a-option>
          </a-select>
          <a-button aria-label="刷新因子结果" @click="refresh"><template #icon><icon-refresh /></template></a-button>
        </a-space>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-empty v-else-if="!selectedSet" description="当前空间没有因子集" />
      <template v-else>
        <div class="status-strip">
          <a-tag size="small" :color="selectedSet.status === 'enabled' ? 'green' : 'orange'">{{ statusLabel(selectedSet.status) }}</a-tag>
          <span>结果数据集 {{ selectedSet.result_dataset_id }}</span>
          <span>Storage 结果 View {{ resultView?.view_id || "准备中" }}</span>
          <span>Python Workers {{ engineStatus.python_workers }}</span>
          <span>忙碌 {{ engineStatus.python_busy }}</span>
          <span>Consumer {{ engineStatus.consumer_running ? "运行中" : "停止" }}</span>
        </div>
        <ViewBrowse
          v-if="resultView"
          :key="`${selectedSpaceId}/${resultView.view_id}`"
          :embedded="true"
          page-title="因子结果数据"
          empty-description="Storage 尚未提供该因子集的默认结果 View"
          :view-ids="[resultView.view_id]"
          :view-owner-modules="['factor']"
          :view-roles="['factor_result']"
          :auto-refresh-interval-ms="60000"
        />
        <a-empty v-else-if="!resultViewLoading" description="Storage 尚未提供该因子集的默认结果 View" />
        <a-spin v-else class="result-view-loading" />
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Message } from "@arco-design/web-vue";
import { getFactorStatus, listFactorSets } from "@/api/factor";
import type { EngineStatus, FactorSetInfo } from "@/api/factor/types";
import { listViews } from "@/api/storage/metadata";
import type { View } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { statusLabel } from "@/views/data/shared/metadata-utils";
import ViewBrowse from "@/views/data/view-browse/index.vue";

defineOptions({ name: "FactorResults" });

const route = useRoute();
const router = useRouter();
const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const allSetInfos = ref<FactorSetInfo[]>([]);
const selectedSetId = ref(String(route.query.set_id || ""));
const setsLoading = ref(false);
const resultView = ref<View | null>(null);
const resultViewLoading = ref(false);
const engineStatus = ref<EngineStatus>({
  ret_info: { code: 0, msg: "" },
  consumer_running: false,
  python_workers: 0,
  python_busy: 0,
  lanes: [],
  recent_runs: []
});
let statusTimer: number | undefined;
let setLoadSequence = 0;
let resultViewSequence = 0;

const availableSets = computed(() => allSetInfos.value.map(info => info.factor_set).filter(set => set?.space_id === selectedSpaceId.value));
const selectedSet = computed(() => availableSets.value.find(set => set.set_id === selectedSetId.value));

async function loadResultView() {
  const set = selectedSet.value;
  const sequence = ++resultViewSequence;
  resultView.value = null;
  if (!set) {
    resultViewLoading.value = false;
    return;
  }
  resultViewLoading.value = true;
  try {
    const rsp = await listViews({
      space_id: set.space_id,
      dataset_id: set.result_dataset_id,
      status: "active",
      page: { page: 1, size: 100 }
    });
    if (sequence !== resultViewSequence || selectedSet.value?.set_id !== set.set_id) return;
    resultView.value = (rsp.views || []).find(view =>
      view.attributes?.owner_module === "factor" && view.attributes?.view_role === "factor_result"
    ) || null;
  } catch (error) {
    if (sequence === resultViewSequence) Message.error(error instanceof Error ? error.message : "结果 View 加载失败");
  } finally {
    if (sequence === resultViewSequence) resultViewLoading.value = false;
  }
}

async function loadSets() {
  const spaceId = selectedSpaceId.value;
  const sequence = ++setLoadSequence;
  if (!spaceId) {
    allSetInfos.value = [];
    selectedSetId.value = "";
    setsLoading.value = false;
    return;
  }
  setsLoading.value = true;
  try {
    const items: FactorSetInfo[] = [];
    for (let page = 1; ; page += 1) {
      const rsp = await listFactorSets({ page: { page, size: 500 } });
      items.push(...(rsp.factor_sets || []));
      if (!rsp.page_result?.has_more || !(rsp.factor_sets || []).length) break;
    }
    if (sequence !== setLoadSequence || selectedSpaceId.value !== spaceId) return;
    allSetInfos.value = items.filter(info => info.factor_set?.space_id === spaceId);
    if (!allSetInfos.value.some(info => info.factor_set.set_id === selectedSetId.value)) {
      selectedSetId.value = allSetInfos.value[0]?.factor_set.set_id || "";
    }
  } catch (error) {
    if (sequence === setLoadSequence) Message.error(error instanceof Error ? error.message : "因子集加载失败");
  } finally {
    if (sequence === setLoadSequence) setsLoading.value = false;
  }
}

async function loadEngineStatus() {
  try {
    engineStatus.value = await getFactorStatus();
  } catch {
    engineStatus.value = {
      ret_info: { code: 0, msg: "" },
      consumer_running: false,
      python_workers: 0,
      python_busy: 0,
      lanes: [],
      recent_runs: []
    };
  }
}

function syncRoute() {
  router.replace({ path: "/factor/results", query: selectedSetId.value ? { set_id: selectedSetId.value } : {} });
}

async function refresh() {
  await Promise.all([loadSets(), loadEngineStatus()]);
}

watch(selectedSpaceId, () => loadSets());
watch(selectedSet, () => loadResultView(), { immediate: true });
watch(() => route.query.set_id, value => {
  const next = String(value || "");
  if (next !== selectedSetId.value) selectedSetId.value = next;
});
onMounted(() => {
  loadSets();
  loadEngineStatus();
  statusTimer = window.setInterval(loadEngineStatus, 5000);
});
onBeforeUnmount(() => {
  if (statusTimer !== undefined) window.clearInterval(statusTimer);
});
</script>

<style scoped>
.factor-results-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.factor-results-workbench > .moox-inner {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
}

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-3);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.status-strip {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-4);
  margin-bottom: var(--moox-space-3);
  color: var(--color-text-2);
  font-size: 13px;
}

.result-view-loading {
  display: flex;
  justify-content: center;
  padding: var(--moox-space-8);
}
</style>
