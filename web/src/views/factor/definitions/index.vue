<template>
  <div class="moox-page definitions-page">
    <div class="moox-inner definitions-inner">
      <a-alert v-if="!spaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="因子定义" @change="onTabChange" />
        <section class="definitions-content">
          <keep-alive>
            <DefinitionsTab
              v-if="activeTab === 'definitions'"
              :space-id="spaceId"
              :categories="categories"
              :tasks="store.tasks"
              :task-label-of="store.taskLabel"
              @categories-changed="loadCategories"
              @tasks-changed="reloadTasks"
              @open-detail="openDetail"
              @open-task="openTask"
            />
            <OutputFieldsTab
              v-else
              :space-id="spaceId"
              :tasks="store.tasks"
              :tasks-ready="store.isLoadedFor(spaceId)"
              :categories="categories"
              :loading="store.loading"
              :load-error="store.loadError"
              :task-label-of="store.taskLabel"
              @open-detail="openDetail"
              @open-task="openTask"
              @retry-tasks="retryTasks"
            />
          </keep-alive>
        </section>
      </template>

      <FactorDetailDrawer
        :factor-id="detailId"
        :categories="categories"
        @close="closeDetail"
        @open-task="openTask"
        @edit="editFactor"
        @view-outputs="viewOutputs"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { listFactorCategories } from "@/api/factor";
import { UNCATEGORIZED, type FactorCategory } from "@/api/factor/types";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { useQueryTab } from "@/hooks/useQueryTab";
import { useFactorScope } from "@/views/factor/shared/use-factor-scope";
import DefinitionsTab from "./definitions-tab.vue";
import FactorDetailDrawer from "./factor-detail-drawer.vue";
import OutputFieldsTab from "./output-fields-tab.vue";

defineOptions({ name: "FactorDefinitions" });

type FactorDefinitionsTabKey = "definitions" | "fields";

const tabs = [
  { key: "definitions", label: "因子定义" },
  { key: "fields", label: "产出字段" }
] as const;

const route = useRoute();
const router = useRouter();
const { store, spaceId } = useFactorScope({ poll: false });

const categories = ref<FactorCategory[]>([]);
// 打开的因子详情写进 ?detail=：刷新、后退、分享链接都能回到同一个因子，两个页签共用同一个详情抽屉。
const detailId = computed(() => (typeof route.query.detail === "string" ? route.query.detail : ""));

function categoryHasOutputFields(categoryId: string): boolean {
  return store.tasks.some(info =>
    (info.members || []).some(member => {
      const hasOutputs = (member.output_columns?.length || 0) > 0 || (member.factor?.outputs?.length || 0) > 0;
      if (!hasOutputs) return false;
      const memberCategory = (member.factor?.category_id || "").trim() || UNCATEGORIZED;
      return memberCategory === categoryId;
    })
  );
}

const { activeTab, setTab: onTabChange } = useQueryTab<FactorDefinitionsTabKey>({
  tabs: ["definitions", "fields"],
  buildQuery(tab, currentQuery) {
    const category = typeof currentQuery.category === "string" ? currentQuery.category : "";
    const query: Record<string, string> = tab === "fields" ? { tab: "fields" } : {};
    // 切到产出字段时，若当前空间任务已加载且所选分类无产出列，不透传空分类避免静默空表。
    if (category && (tab === "definitions" || !store.isLoadedFor(spaceId.value) || categoryHasOutputFields(category))) {
      query.category = category;
    }
    if (detailId.value) query.detail = detailId.value;
    return query;
  }
});

/** 分类只用于归类与筛选；加载失败时分类显示为 ID，不影响列表。 */
async function loadCategories() {
  try {
    categories.value = await listFactorCategories();
  } catch {
    categories.value = [];
  }
}

function reloadTasks() {
  void store.reload({ silent: true });
}

function openDetail(factorId: string) {
  void router.replace({ path: route.path, query: { ...route.query, detail: factorId } });
}

function closeDetail() {
  const query = { ...route.query };
  delete query.detail;
  void router.replace({ path: route.path, query });
}

function openTask(taskId: string) {
  void router.push({ path: "/factor/tasks", query: { detail: taskId } });
}

function editFactor(factorId: string) {
  void router.push(`/factor/definitions/${encodeURIComponent(factorId)}/edit`);
}

function viewOutputs(taskId: string, keyword: string) {
  const query: Record<string, string> = { tab: "fields" };
  if (taskId) query.task = taskId;
  if (keyword) query.keyword = keyword;
  void router.replace({ path: route.path, query });
}

function retryTasks() {
  void store.reload({ silent: false });
}

// 切空间时加载分类，并清掉旧空间专属的 URL 筛选，保持当前页签。
watch(
  spaceId,
  (next, previous) => {
    if (next) void loadCategories();
    else categories.value = [];
    if (!previous || next === previous || route.path !== "/factor/definitions") return;
    const query: Record<string, string> = activeTab.value === "fields" ? { tab: "fields" } : {};
    void router.replace({ path: route.path, query });
  },
  { immediate: true, flush: "sync" }
);
</script>

<style scoped>
.definitions-page {
  height: 100%;
  min-height: 0;
}

.definitions-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.definitions-content {
  min-width: 0;
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
}
</style>
