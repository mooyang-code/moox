<template>
  <div class="view-result-table"><slot /></div>
</template>

<script setup lang="ts">
defineProps<{ loading?: boolean; rows?: unknown[] }>();
defineEmits<{ select: [row: unknown] }>();
</script>
