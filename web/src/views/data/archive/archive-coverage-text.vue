<template>
  <div v-if="coverage" class="archive-coverage">
    <router-link :to="coverage.link" :class="`tone-${coverage.tone}`" @click.stop>{{ coverage.text }}</router-link>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useSpaceStore } from "@/store/modules/space";
import { useArchiveCoverage } from "./archive-coverage";

/** 采集任务页里的一行小字：已归档 · 健康等级，或未归档；读不到时什么都不显示。链接到归档页。 */
const props = defineProps<{ datasetId?: string }>();
const spaceStore = useSpaceStore();
const coverageOf = useArchiveCoverage(computed(() => spaceStore.selectedSpaceId || ""));
const coverage = computed(() => coverageOf.value(props.datasetId));
</script>

<style scoped>
.archive-coverage {
  font-size: 12px;
  line-height: 18px;
}

.archive-coverage a {
  text-decoration: none;
}

.tone-muted {
  color: var(--color-text-3);
}

.tone-warn {
  color: rgb(var(--orange-6));
}

.tone-bad {
  color: rgb(var(--red-6));
}

.tone-ok {
  color: rgb(var(--green-6));
}
</style>
