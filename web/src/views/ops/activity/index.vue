<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-radio-group v-model="scope" type="button" class="activity-scope">
        <a-radio value="space">当前空间</a-radio>
        <a-radio value="global">全局</a-radio>
      </a-radio-group>
      <a-alert v-if="scope === 'space' && !selectedSpaceId" type="warning">请先在顶部选择空间。</a-alert>
      <ActivityLog v-else :space-id="selectedSpaceId" :global="scope === 'global'" :page-size="20" show-filters />
    </div>
  </div>
</template>

<script setup lang="ts">
import { storeToRefs } from "pinia";
import { ref } from "vue";
import ActivityLog from "@/components/activity-log/index.vue";
import { useSpaceStore } from "@/store/modules/space";

const { selectedSpaceId } = storeToRefs(useSpaceStore());
const scope = ref<"space" | "global">("space");
</script>

<style scoped>
.activity-scope {
  margin-bottom: var(--moox-space-2);
}
</style>
