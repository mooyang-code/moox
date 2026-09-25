<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>数据对象</h2>
        <span class="page-head__hint">用标签管理对象范围，并在采集任务中复用</span>
      </div>

      <a-alert v-if="!selectedSpaceId" type="warning" show-icon>请先在顶部选择空间</a-alert>
      <a-tabs v-else v-model:active-key="activeTab" class="subject-tabs">
        <a-tab-pane key="tags" title="标签">
          <TagsTab :space-id="selectedSpaceId" @open-members="openMembers" />
        </a-tab-pane>
        <a-tab-pane key="members" title="标签成员">
          <MembersTab :space-id="selectedSpaceId" :initial-tag-id="selectedTagId" />
        </a-tab-pane>
      </a-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { useSpaceStore } from "@/store/modules/space";
import MembersTab from "./members-tab.vue";
import TagsTab from "./tags-tab.vue";

defineOptions({ name: "DataSubjects" });

const spaceStore = useSpaceStore();
const selectedSpaceId = computed(() => spaceStore.selectedSpaceId);
const activeTab = ref("tags");
const selectedTagId = ref("");

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
.page-head {
  display: flex;
  align-items: baseline;
  gap: var(--moox-space-3);
  margin-bottom: var(--moox-space-2);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.page-head__hint {
  color: var(--color-text-3);
  font-size: 13px;
}

.subject-tabs {
  min-width: 0;
}
</style>
