<template>
  <a-tooltip v-model:popup-visible="visible" :content="props.text" position="bottom">
    <button class="info-tip" type="button" aria-label="说明" @focus="visible = true" @blur="visible = false">
      <icon-info-circle />
    </button>
  </a-tooltip>
</template>

<script setup lang="ts">
import { ref } from "vue";

defineOptions({ name: "FactorInfoTip" });

const props = defineProps<{ text: string }>();
const visible = ref(false);
</script>

<style scoped>
.info-tip {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  color: var(--color-text-3);
  background: transparent;
  border: 0;
  border-radius: 4px;
  cursor: help;
}

.info-tip:hover {
  color: var(--color-text-2);
}

.info-tip:focus-visible {
  outline: 2px solid rgb(var(--primary-6));
  outline-offset: 2px;
}
</style>
