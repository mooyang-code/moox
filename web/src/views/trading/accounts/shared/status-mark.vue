<template>
  <span class="tx-st" :class="`tx-st--${tone}`"><slot /></span>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ tone?: "ok" | "warn" | "bad" | "run" | "" }>(), { tone: "" });
</script>

<style src="./accounts.css"></style>
