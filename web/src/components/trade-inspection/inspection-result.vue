<template>
  <div class="inspection-result">
    <a-alert v-if="verdict === 'blocked'" type="error" class="inspection-alert" :show-icon="true">
      <template #title>检测发现阻断项，不能用这把密钥建执行账户</template>
      <ul class="inspection-findings">
        <li v-for="item in view.blockers" :key="item.code + item.message">{{ item.message }}</li>
      </ul>
    </a-alert>
    <a-alert v-else-if="verdict === 'skipped'" type="info" class="inspection-alert" :show-icon="true">
      测试网跳过权限与类型检测，一个测试网账户只能开通一个市场。
    </a-alert>
    <a-alert v-else type="success" class="inspection-alert" :show-icon="true">检测通过。</a-alert>

    <a-alert v-if="failureNote" type="warning" class="inspection-alert" :show-icon="true">{{ failureNote }}</a-alert>
    <a-alert v-for="item in view.warnings" :key="item.code + item.message" type="warning" class="inspection-alert">
      {{ item.message }}
    </a-alert>

    <a-descriptions :column="1" size="small" bordered class="inspection-rows">
      <a-descriptions-item v-for="row in rows" :key="row.label" :label="row.label">{{ row.value }}</a-descriptions-item>
      <a-descriptions-item v-if="view.inspectedAt" label="检测时间">{{
        formatInstant(view.inspectedAt, zone)
      }}</a-descriptions-item>
    </a-descriptions>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { formatInstant } from "@/utils/time-zone";
import { inspectionFailureNote, inspectionRows, inspectionVerdict, type InspectionView } from "./inspection";

defineOptions({ name: "TradeInspectionResult" });

// zone 由调用方给：交易页按空间时区，密钥页按 UTC
const props = defineProps<{ view: InspectionView; zone: string }>();

const verdict = computed(() => inspectionVerdict(props.view));
const rows = computed(() => inspectionRows(props.view));
const failureNote = computed(() => inspectionFailureNote(props.view));
</script>

<style scoped>
.inspection-alert {
  margin-bottom: 8px;
}
.inspection-findings {
  margin: 4px 0 0;
  padding-left: 18px;
}
.inspection-rows {
  margin-top: 4px;
}
</style>
