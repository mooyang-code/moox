<template>
  <div class="moox-page data-subjects-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据对象" @change="onTabChange" />
        <section class="subjects-content">
          <keep-alive>
            <TagsTab v-if="activeTab === 'tags'" :space-id="selectedSpaceId" @openMembers="openMembers" />
            <MembersTab v-else :space-id="selectedSpaceId" :initial-tag-id="selectedTagId" @tag-change="openMembers" />
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
import { useSpaceStore } from "@/store/modules/space";
import MembersTab from "./members-tab.vue";
import TagsTab from "./tags-tab.vue";

defineOptions({ name: "DataSubjects" });

type SubjectTab = "tags" | "members";

const tabs = [
  { key: "tags", label: "标签" },
  { key: "members", label: "标签成员" }
] as const;

const spaceStore = useSpaceStore();
const route = useRoute();
const router = useRouter();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
// 页签和所选标签都在 URL 里（?tab=members&tag=）：刷新、后退、分享链接都能回到同一处
const activeTab = computed<SubjectTab>(() => (route.query.tab === "members" ? "members" : "tags"));
const selectedTagId = computed(() => (typeof route.query.tag === "string" ? route.query.tag : ""));

function writeQuery(tab: SubjectTab, tagId: string) {
  const query = { ...route.query, tab: tab === "members" ? "members" : undefined, tag: tagId || undefined };
  void router.replace({ path: route.path, query });
}

function onTabChange(value: string | number) {
  writeQuery(value === "members" ? "members" : "tags", selectedTagId.value);
}

function openMembers(tagId: string) {
  writeQuery("members", tagId);
}

// 换空间：标签属于原来的空间，回到标签页签
watch(selectedSpaceId, (next, previous) => {
  if (previous && next !== previous && route.path === "/data/subjects") writeQuery("tags", "");
});
</script>

<style scoped>
.data-subjects-page {
  height: 100%;
  min-height: 0;
}

.data-subjects-page > .moox-inner {
  display: flex;
  min-height: 100%;
  flex-direction: column;
}

.subjects-content {
  min-width: 0;
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
}
</style>
