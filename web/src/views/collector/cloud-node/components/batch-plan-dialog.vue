<template><slot /></template>

<script setup lang="ts">
defineProps<{ visible: boolean; loading?: boolean; total?: number }>();
defineEmits<{ confirm: []; cancel: [] }>();
</script>
