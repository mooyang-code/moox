<template>
  <div class="moox-page data-subjects-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <template v-else>
        <PageTitleTabs :model-value="activeTab" :items="tabs" aria-label="数据对象" @change="onTabChange" />
        <section class="subjects-content">
          <keep-alive>
            <TagsTab v-if="activeTab === 'tags'" :space-id="selectedSpaceId" @open-members="openMembers" />
            <MembersTab v-else :space-id="selectedSpaceId" :initial-tag-id="selectedTagId" />
          </keep-alive>
        </section>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
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
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const activeTab = ref<SubjectTab>("tags");
const selectedTagId = ref("");

function onTabChange(value: string | number) {
  activeTab.value = value === "members" ? "members" : "tags";
}

function openMembers(tagId: string) {
  selectedTagId.value = tagId;
  activeTab.value = "members";
}

watch(selectedSpaceId, () => {
  selectedTagId.value = "";
  activeTab.value = "tags";
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
