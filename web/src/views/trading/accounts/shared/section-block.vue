<template>
  <section class="tx-section">
    <header class="tx-section-head">
      <h3>{{ title }}</h3>
      <span v-if="$slots.sub" class="tx-muted tx-section-sub"><slot name="sub" /></span>
      <span class="tx-section-spacer" />
      <slot name="extra" />
    </header>
    <div class="tx-section-body" :class="{ 'tx-section-body--flush': flush }">
      <slot />
    </div>
  </section>
</template>

<script setup lang="ts">
defineProps<{ title: string; flush?: boolean }>();
</script>

<style src="./accounts.css"></style>
<style scoped>
.tx-section {
  border: 1px solid var(--color-border-2);
  border-radius: var(--border-radius-small);
}

.tx-section-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--moox-space-2);
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border-2);
}

.tx-section-head h3 {
  margin: 0;
  font-size: 15px;
}

.tx-section-sub {
  font-size: 12px;
}

.tx-section-spacer {
  flex: 1;
}

.tx-section-body {
  padding: 12px 16px;
}

.tx-section-body--flush {
  padding: 0;
}
</style>
