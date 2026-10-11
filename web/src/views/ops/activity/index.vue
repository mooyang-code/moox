<template>
  <div class="moox-page">
    <div class="moox-inner">
      <a-alert v-if="!selectedSpaceId" type="warning">请先在顶部选择空间。</a-alert>
      <ActivityLog v-else :space-id="selectedSpaceId" :page-size="20" show-filters />
    </div>
  </div>
</template>

<script setup lang="ts">
import { storeToRefs } from "pinia";
import ActivityLog from "@/components/activity-log/index.vue";
import { useSpaceStore } from "@/store/modules/space";

const { selectedSpaceId } = storeToRefs(useSpaceStore());
</script>
