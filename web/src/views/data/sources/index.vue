<template>
  <div class="moox-page data-sources-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据源" @change="onTabChange" />
        <section class="sources-content">
          <keep-alive>
            <SourcesTab v-if="activeTab === 'sources'" :space-id="selectedSpaceId" @open-fields="openFieldsForProvider" />
            <FieldsTab v-else :space-id="selectedSpaceId" />
          </keep-alive>
        </section>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import { useQueryTab } from "@/hooks/useQueryTab";
import { useSpaceStore } from "@/store/modules/space";
import FieldsTab from "./fields-tab.vue";
import SourcesTab from "./sources-tab.vue";

defineOptions({ name: "DataSources" });

type DataSourceTab = "sources" | "fields";

const tabs = [
  { key: "sources", label: "数据源" },
  { key: "fields", label: "字段定义" }
] as const;

const spaceStore = useSpaceStore();
const route = useRoute();
const router = useRouter();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const { activeTab, setTab: onTabChange } = useQueryTab<DataSourceTab>({
  tabs: ["sources", "fields"],
  buildQuery(tab, currentQuery) {
    if (tab === "sources") return {};
    const rest = Object.fromEntries(Object.entries(currentQuery).filter(([key]) => key !== "tab"));
    return { ...rest, tab: "fields" };
  }
});

function openFieldsForProvider(providerId: string) {
  const query: Record<string, string> = { tab: "fields" };
  if (providerId.trim()) query.provider = providerId.trim();
  void router.replace({ path: route.path, query });
}

// 换空间：清掉旧空间的筛选参数，保持当前所在页签。
watch(
  selectedSpaceId,
  (next, previous) => {
    if (!previous || next === previous || route.path !== "/data/sources") return;
    const query = activeTab.value === "fields" ? { tab: "fields" } : {};
    void router.replace({ path: route.path, query });
  },
  { flush: "sync" }
);
</script>

<style scoped>
.data-sources-page {
  height: 100%;
  min-height: 0;
}

.data-sources-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.sources-content {
  min-width: 0;
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
}
</style>
