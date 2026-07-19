<template><slot /></template>

<script setup lang="ts">
defineProps<{ visible: boolean; loading?: boolean; batch?: boolean }>();
defineEmits<{ confirm: []; cancel: [] }>();
</script>
