<template>
  <details v-if="value" class="raw-error">
    <summary>原始错误</summary>
    <pre>{{ value }}</pre>
  </details>
</template>
<script setup lang="ts">
defineProps<{ value?: string }>();
</script>
<style scoped>
.raw-error {
  margin-top: 12px;
}
summary {
  cursor: pointer;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  padding: 12px;
  background: var(--color-fill-1);
}
</style>
