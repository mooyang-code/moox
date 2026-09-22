<template>
  <div class="view-result-table"><slot /></div>
</template>

<script setup lang="ts">
defineProps<{ loading?: boolean; rows?: unknown[] }>();
defineEmits<{ select: [row: unknown] }>();
</script>

<style scoped>
.view-result-table {
  width: 100%;
  max-width: 100%;
  min-width: 0;
}
</style>
