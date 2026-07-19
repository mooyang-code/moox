<template><slot /></template>

<script setup lang="ts">
defineProps<{ visible: boolean; row?: unknown }>();
defineEmits<{ close: [] }>();
</script>
