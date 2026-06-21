<template>
  <div class="moox-page">
    <div class="moox-inner">
      <SpaceContextBar />
      <a-empty description="交易明细 - 开发中" />
    </div>
  </div>
</template>

<script setup lang="ts">
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
defineOptions({ name: "trade-record" });
</script>
