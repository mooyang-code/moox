<template>
  <a-tag size="small" :color="health.color">{{ health.label }}</a-tag>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { FactorTask, TaskRunSummary } from "@/api/factor/types";
import { setHealth } from "./health";

defineOptions({ name: "FactorHealthTag" });

const props = defineProps<{ set: Pick<FactorTask, "status" | "freq">; lastRun?: TaskRunSummary }>();
const health = computed(() => setHealth(props.set, props.lastRun));
</script>
