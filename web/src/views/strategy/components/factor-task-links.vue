<template>
  <span class="factor-task-links"
    ><template v-for="(item, index) in items" :key="item.market"
      >{{ index ? "、" : "" }}<template v-if="items.length > 1">{{ `${taskKeyLabel(item.market)} ` }}</template
      ><a class="mono" :href="`#/factor/tasks?detail=${encodeURIComponent(item.taskId)}`" @click.stop>{{
        item.taskId
      }}</a></template
    ><template v-if="!items.length">-</template></span
  >
</template>

<script setup lang="ts">
import { computed } from "vue";
import { instanceTasks, taskKeyLabel } from "@/views/strategy/market-mode";

defineOptions({ name: "FactorTaskLinks" });

/** 实例绑定的计算任务：每个任务链到计算任务页的详情（?detail=）。 */
const props = defineProps<{ tasks: Record<string, string> | undefined }>();
const items = computed(() => instanceTasks(props.tasks));
</script>
