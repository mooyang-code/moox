<template>
  <div class="cloud-node-table">
    <slot />
  </div>
</template>

<script setup lang="ts">
defineProps<{ loading?: boolean; selectedCount?: number }>();
defineEmits<{
  select: [keys: string[]];
  pageChange: [page: number];
}>();
</script>
