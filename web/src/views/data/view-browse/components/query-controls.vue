<template>
  <div class="view-query-controls"><slot /></div>
</template>

<script setup lang="ts">
defineProps<{ loading?: boolean; mode?: string }>();
defineEmits<{ submit: []; reset: [] }>();
</script>
