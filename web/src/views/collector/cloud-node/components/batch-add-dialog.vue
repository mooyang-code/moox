<template><slot /></template>

<script setup lang="ts">
defineProps<{ visible: boolean; loading?: boolean }>();
defineEmits<{ confirm: []; cancel: [] }>();
</script>
