<template>
  <div class="equity-curve">
    <div ref="container" class="chart" />
    <a-empty v-if="!loading && !points.length" description="暂无资金曲线" />
  </div>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import { default as VChart } from "@visactor/vchart";
import { queryEquityCurve, type EquityPoint } from "@/api/trade";
import { toEquitySeries } from "./equity-curve";

const props = defineProps<{ logicalAccountId: string }>();
const container = ref<HTMLElement>();
const points = ref<EquityPoint[]>([]);
const loading = ref(false);
let chart: VChart | null = null;
let requestSeq = 0;

async function load() {
	const seq = ++requestSeq;
	const logicalAccountId = props.logicalAccountId;
	loading.value = true;
	points.value = [];
	chart?.release();
	chart = null;
	try {
		const response = await queryEquityCurve({ logical_account_id: logicalAccountId });
		if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId) return;
		points.value = response.points || [];
		await nextTick();
		if (seq !== requestSeq || logicalAccountId !== props.logicalAccountId || !container.value) return;
		chart = new VChart({
			type: "line",
			data: [{ id: "equity", values: toEquitySeries(points.value) }],
			xField: "time",
			yField: "value",
			seriesField: undefined,
			axes: [{ orient: "bottom", type: "time" }, { orient: "left", type: "linear" }],
			line: { style: { lineWidth: 2 } },
			point: { visible: false }
		}, { dom: container.value });
		chart.renderSync();
	} finally {
		if (seq === requestSeq) loading.value = false;
	}
}

watch(() => props.logicalAccountId, load, { immediate: true });
onBeforeUnmount(() => chart?.release());
</script>

<style scoped>
.chart { min-height: 260px; width: 100%; }
</style>
