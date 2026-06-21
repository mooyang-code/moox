<template>
  <div class="moox-page">
    <div class="moox-inner">
      <SpaceContextBar />
      <a-empty description="策略列表 - 开发中" />
    </div>
  </div>
</template>

<script setup lang="ts">
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
defineOptions({ name: "strategy-list" });
</script>
