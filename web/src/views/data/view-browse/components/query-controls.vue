<template>
  <div class="view-query-controls"><slot /></div>
</template>

<script setup lang="ts">
defineProps<{ loading?: boolean; mode?: string }>();
defineEmits<{ submit: []; reset: [] }>();
</script>

<style scoped>
.view-query-controls {
  width: 100%;
  max-width: 100%;
  min-width: 0;
}
</style>
