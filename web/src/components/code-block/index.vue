<template>
  <section class="code-block" :aria-label="`${language} 源码`">
    <header class="code-toolbar">
      <span class="code-language">{{ language }}</span>
      <a-button size="mini" type="text" :loading="copying" @click="copyCode">
        <template #icon><icon-copy /></template>
        复制
      </a-button>
    </header>
    <pre><code>{{ code || "暂无源码" }}</code></pre>
  </section>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { Message } from "@arco-design/web-vue";

defineOptions({ name: "CodeBlock" });

const props = withDefaults(
  defineProps<{
    code?: string;
    language?: string;
  }>(),
  { code: "", language: "text" }
);

const copying = ref(false);

async function copyCode() {
  if (!props.code) {
    Message.warning("暂无可复制的源码");
    return;
  }
  if (!navigator.clipboard) {
    Message.warning("当前浏览器不支持复制");
    return;
  }
  copying.value = true;
  try {
    await navigator.clipboard.writeText(props.code);
    Message.success("源码已复制");
  } catch {
    Message.error("复制源码失败");
  } finally {
    copying.value = false;
  }
}
</script>

<style scoped>
.code-block {
  overflow: hidden;
  border: 1px solid var(--color-border-2);
  border-radius: 4px;
  background: var(--color-fill-1);
}

.code-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  min-height: 36px;
  padding: 0 8px 0 12px;
  border-bottom: 1px solid var(--color-border-2);
  background: var(--color-fill-2);
}

.code-language {
  color: var(--color-text-3);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  text-transform: lowercase;
}

pre {
  max-height: 58vh;
  margin: 0;
  overflow: auto;
  padding: 14px 16px;
  color: var(--color-text-1);
  background: var(--color-bg-2);
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre;
}
</style>
