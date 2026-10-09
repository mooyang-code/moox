<template>
  <div v-if="text" class="raw-error">
    <a-link class="raw-error__toggle" :hoverable="false" @click.stop="expanded = !expanded">
      {{ expanded ? "收起原始错误" : "查看原始错误" }}
    </a-link>
    <pre v-if="expanded" class="raw-error__text">{{ text }}</pre>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";

defineProps<{ text?: string }>();
const expanded = ref(false);
</script>

<style scoped lang="scss">
.raw-error__toggle {
  padding: 0;
  font-size: 12px;
}

.raw-error__text {
  max-height: 240px;
  margin: var(--moox-space-1) 0 0;
  padding: var(--moox-space-2);
  overflow: auto;
  border-radius: 4px;
  background: var(--color-fill-2);
  color: var(--color-text-2);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
