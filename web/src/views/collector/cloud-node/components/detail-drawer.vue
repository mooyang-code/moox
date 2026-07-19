<template><slot /></template>

<script setup lang="ts">
defineProps<{ visible: boolean; node?: unknown }>();
defineEmits<{ close: [] }>();
</script>
