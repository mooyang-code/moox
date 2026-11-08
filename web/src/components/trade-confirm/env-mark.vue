<template>
  <span class="tx-env" :class="`tx-env--${env}`">{{ environmentLabel(env) }}</span>
</template>

<script setup lang="ts">
import { environmentLabel, type Environment } from "./environment";

defineProps<{ env: Environment }>();
</script>

<style src="./trade-confirm.css"></style>
