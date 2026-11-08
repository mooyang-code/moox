<template>
  <div class="event-info">
    <p v-if="event.description" class="event-info__desc">{{ event.description }}</p>
    <template v-if="event.payload_fields.length">
      <h4 class="event-info__title">data 里的字段</h4>
      <a-table :data="event.payload_fields" row-key="field" size="small" :pagination="false" :bordered="{ cell: true }">
        <template #columns>
          <a-table-column title="字段" data-index="field" :width="180" />
          <a-table-column title="说明" data-index="title" />
        </template>
      </a-table>
    </template>
    <template v-if="sample">
      <h4 class="event-info__title">示例请求体</h4>
      <CodeBlock :code="sample" language="json" class="event-info__sample" />
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { SubscribableEvent } from "@/api/callback-dispatcher";
import CodeBlock from "@/components/code-block/index.vue";
import { prettyPayload } from "./event-center-utils";

defineOptions({ name: "EventInfo" });

/** 一个事件的说明、data 字段和示例请求体。请求头和请求体外层字段由“接收说明”讲，这里不重复。 */
const props = defineProps<{ event: SubscribableEvent }>();

// 旧版本的服务不返回示例和字段说明：只显示说明。
const sample = computed(() => prettyPayload(props.event.sample_json));
</script>

<style scoped>
.event-info__desc {
  margin: 0 0 8px;
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 1.7;
}

/* 示例较长：限制高度，弹窗里下面的表单不被挤得太远 */
.event-info__sample :deep(pre) {
  max-height: 240px;
}

.event-info__title {
  margin: 16px 0 8px;
  font-size: 14px;
  font-weight: 600;
}
</style>
