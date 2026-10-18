<template>
  <a-modal :visible="Boolean(fn)" :width="760" :footer="false" title="函数详情" unmount-on-close @cancel="emit('close')">
    <template v-if="fn">
      <div class="detail-head">
        <span class="mono-text function-name">{{ fn.name }}</span>
        <a-tooltip content="复制函数名">
          <a-button size="mini" type="text" aria-label="复制函数名" @click="copyName">
            <template #icon><icon-copy /></template>
          </a-button>
        </a-tooltip>
        <a-tag size="small" :color="functionStateInfo(fn.state).color">{{ functionStateInfo(fn.state).text }}</a-tag>
      </div>
      <a-descriptions :column="2" bordered size="small">
        <a-descriptions-item label="地域">{{ fn.region || "-" }}</a-descriptions-item>
        <a-descriptions-item label="触发"
          >{{ cronText(fn.cron) }}<span v-if="fn.cron" class="mono-text muted"> （{{ fn.cron }}）</span></a-descriptions-item
        >
        <a-descriptions-item label="市场" :span="2"
          ><span class="mono-text">{{ fn.market || "-" }}</span></a-descriptions-item
        >
        <a-descriptions-item label="标的数">{{ fn.subject_count || 0 }}</a-descriptions-item>
        <a-descriptions-item label="服务的数据集">{{ (fn.datasets || []).length }} 个</a-descriptions-item>
      </a-descriptions>

      <div class="subjects-head">
        <h4>采集的标的</h4>
        <a-input v-model="keyword" placeholder="搜索标的" allow-clear size="small" style="width: 200px">
          <template #prefix><icon-search /></template>
        </a-input>
      </div>
      <div v-if="!groups.length" class="empty-text">该函数没有分配标的</div>
      <section v-for="group in groups" :key="group.dataset" class="subject-group">
        <div class="group-title">
          <span>{{ group.label }}</span>
          <span class="muted"
            >{{ group.total }} 个标的<template v-if="group.effectiveFrom">
              · {{ localTimeText(group.effectiveFrom) }} 起生效</template
            ></span
          >
        </div>
        <div class="subject-list">
          <a-tag v-for="subject in group.subjects" :key="subject" size="small" class="subject-tag">{{ subject }}</a-tag>
          <span v-if="!group.subjects.length" class="muted">没有匹配的标的</span>
        </div>
      </section>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import type { CollectorFunctionStatus } from "@/api/collector/runtime-status";
import { cronText, datasetLabel, functionStateInfo, localTimeText } from "./runtime-status-model";

const props = defineProps<{ fn: CollectorFunctionStatus | null; datasetNames: Map<string, string> }>();
const emit = defineEmits<{ (event: "close"): void }>();

const keyword = ref("");
watch(
  () => props.fn?.name,
  () => (keyword.value = "")
);

/** 按数据集分组列出标的，搜索不区分大小写。 */
const groups = computed(() => {
  const needle = keyword.value.trim().toUpperCase();
  return (props.fn?.dataset_subjects || []).map(item => {
    const all = [...(item.subjects || [])].sort();
    return {
      dataset: item.dataset || "",
      label: datasetLabel(props.datasetNames, item.dataset || ""),
      total: all.length,
      effectiveFrom: item.effective_from || "",
      subjects: needle ? all.filter(subject => subject.toUpperCase().includes(needle)) : all
    };
  });
});

async function copyName() {
  try {
    await navigator.clipboard.writeText(props.fn?.name || "");
    Message.success("已复制函数名");
  } catch {
    Message.error("复制失败，请手动选择函数名复制");
  }
}
</script>

<style scoped>
.detail-head {
  display: flex;
  align-items: center;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-3);
}
.function-name {
  font-size: 13px;
  word-break: break-all;
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.subjects-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: var(--moox-space-4) 0 var(--moox-space-2);
}
.subjects-head h4 {
  margin: 0;
  font-size: 13px;
}
.subject-group + .subject-group {
  margin-top: var(--moox-space-3);
}
.group-title {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-1);
  font-size: 13px;
}
.subject-list {
  display: flex;
  max-height: 200px;
  flex-wrap: wrap;
  gap: 4px;
  overflow-y: auto;
  padding: var(--moox-space-2);
  border-radius: 6px;
  background: var(--color-fill-1);
}
.subject-tag {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.empty-text {
  padding: var(--moox-space-3) 0;
  color: var(--color-text-3);
  text-align: center;
}
</style>
