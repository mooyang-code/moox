<template>
  <SvgIcon :name="props.svgIcon" :size="25" />
</template>

<script setup lang="ts">
const props = defineProps<{ svgIcon: string }>();
</script>
