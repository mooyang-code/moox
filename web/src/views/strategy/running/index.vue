<template>
  <div class="running-page">
    <StrategyOverview />
  </div>
</template>

<script setup lang="ts">
import StrategyOverview from "@/views/strategy/overview/index.vue";
defineOptions({ name: "StrategyRunning" });
</script>

<style scoped>
.running-page {
  min-height: 100%;
}
</style>
