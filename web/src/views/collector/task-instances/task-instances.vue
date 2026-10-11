<template>
  <div class="moox-page">
    <div class="moox-inner collector-instances">
      <a-alert v-if="!selectedSpaceId" type="warning">请先在顶部选择空间。</a-alert>
      <template v-else>
        <section class="instances-block">
          <header class="block-header">
            <h3>分配</h3>
            <a-space>
              <a-tag v-if="plans.open" color="orangered">变更进行中：计划 {{ plans.open.id }}（{{ plans.open.status }}）</a-tag>
              <a-tag v-else-if="plans.applied" color="green">计划 {{ plans.applied.id }} 已生效</a-tag>
              <a-tag v-else color="gray">尚无采集计划</a-tag>
              <span class="muted">最近下发：{{ formatDateTime(lastUpdated) }}</span>
              <a-button size="small" :loading="plansLoading" @click="loadPlans">刷新</a-button>
            </a-space>
          </header>
          <a-alert v-if="planError" type="error" class="block-alert">最近一次下发失败：{{ planError }}</a-alert>
          <h4 class="sub-title">数据集</h4>
          <a-table row-key="key" size="small" :data="datasetRows" :pagination="false" :bordered="{ cell: true }" :loading="plansLoading">
            <template #columns>
              <a-table-column title="数据集" data-index="dataset_id" :width="260" />
              <a-table-column title="频率" data-index="frequency" :width="80" />
              <a-table-column title="标的数" data-index="subject_count" :width="90" />
              <a-table-column title="标的集合 hash" :width="140">
                <template #cell="{ record }">
                  <a-tooltip :content="record.hash"
                    ><span class="mono-text">{{ shortHash(record.hash) }}</span></a-tooltip
                  >
                </template>
              </a-table-column>
              <a-table-column title="最近一次生效周期">
                <template #cell="{ record }">{{ record.effective_period ? formatDateTime(record.effective_period) : "-" }}</template>
              </a-table-column>
            </template>
          </a-table>
          <h4 class="sub-title">函数</h4>
          <a-table row-key="name" size="small" :data="plans.functions || []" :pagination="false" :bordered="{ cell: true }" :loading="plansLoading">
            <template #columns>
              <a-table-column title="函数" :width="300">
                <template #cell="{ record }"
                  ><span class="mono-text">{{ record.name }}</span></template
                >
              </a-table-column>
              <a-table-column title="地域" data-index="region" :width="120" />
              <a-table-column title="市场" data-index="market" :width="200" />
              <a-table-column title="触发" data-index="cron" :width="160" />
              <a-table-column title="标的数" data-index="subject_count" :width="90" />
              <a-table-column title="服务的数据集">
                <template #cell="{ record }">
                  <a-tag v-for="dataset in record.datasets || []" :key="dataset" size="small">{{ dataset }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag size="small" :color="functionStateInfo(record.state).color">{{ functionStateInfo(record.state).text }}</a-tag>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>

        <section class="instances-block">
          <header class="block-header">
            <h3>标的变更日志</h3>
          </header>
          <ActivityLog :space-id="selectedSpaceId" category="collector.subject_change" :page-size="10" />
        </section>

        <section class="instances-block">
          <header class="block-header">
            <h3>补采</h3>
            <a-space>
              <a-button size="small" @click="openCheck">检查缺口</a-button>
              <a-button size="small" type="primary" status="success" @click="openBackfill">新建补采</a-button>
              <a-button size="small" :loading="jobsLoading" @click="loadJobs">刷新</a-button>
            </a-space>
          </header>
          <a-table
            row-key="job_id"
            size="small"
            :data="jobs"
            :pagination="{ current: jobsPage, pageSize: jobsPageSize, total: jobsTotal, showTotal: true, size: 'small' }"
            :bordered="{ cell: true }"
            :loading="jobsLoading"
            @page-change="onJobsPageChange"
          >
            <template #columns>
              <a-table-column title="作业" :width="300">
                <template #cell="{ record }"
                  ><span class="mono-text">{{ record.job_id }}</span></template
                >
              </a-table-column>
              <a-table-column title="来源" :width="80">
                <template #cell="{ record }">{{ record.source === "auto" ? "自动" : "手动" }}</template>
              </a-table-column>
              <a-table-column title="状态" :width="100" align="center">
                <template #cell="{ record }">
                  <a-tag size="small" :color="jobStatusInfo(record.status).color">{{ jobStatusInfo(record.status).text }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="进度" :width="220">
                <template #cell="{ record }">
                  <a-progress :percent="backfillProgress(record) / 100" size="small" />
                  <span class="muted"
                    >成功 {{ record.succeeded || 0 }} / 失败 {{ record.failed || 0 }} / 未完成 {{ (record.pending || 0) + (record.running || 0) }}</span
                  >
                </template>
              </a-table-column>
              <a-table-column title="创建时间" :width="180">
                <template #cell="{ record }">{{ formatDateTime(record.created_at) }}</template>
              </a-table-column>
              <a-table-column title="错误">
                <template #cell="{ record }"
                  ><span class="job-error">{{ record.last_error || "-" }}</span></template
                >
              </a-table-column>
              <a-table-column title="操作" :width="90" align="center">
                <template #cell="{ record }">
                  <a-button v-if="backfillCancelable(record)" size="mini" status="danger" @click="onCancel(record)">取消</a-button>
                </template>
              </a-table-column>
            </template>
          </a-table>
        </section>
      </template>
    </div>

    <a-modal v-model:visible="rangeVisible" :title="rangeMode === 'check' ? '检查缺口' : '新建补采'" :ok-loading="rangeBusy" @ok="onRangeSubmit">
      <a-form :model="range" layout="vertical">
        <a-form-item label="数据集（留空表示全部）"><a-input v-model="range.dataset_id" allow-clear /></a-form-item>
        <a-form-item label="标的（留空表示全部）"><a-input v-model="range.subject_id" allow-clear /></a-form-item>
        <a-form-item label="起始时间（UTC，含）" extra="如 2026-10-12T08:00:00Z"><a-input v-model="range.from" /></a-form-item>
        <a-form-item label="结束时间（UTC，不含）"><a-input v-model="range.to" /></a-form-item>
        <a-form-item v-if="rangeMode === 'backfill'"
          ><a-checkbox v-model="range.force">强制重拉（不检查缺口，整个范围重拉）</a-checkbox></a-form-item
        >
        <a-form-item v-else><a-checkbox v-model="range.deep">深度检查（校验字段齐全）</a-checkbox></a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:visible="gapsVisible" :footer="false" width="900px" title="缺口检查结果">
      <p>
        应有 {{ gapResult.expected || 0 }} 根，缺失 {{ gapResult.missing || 0 }} 根<span v-if="gapResult.truncated">（结果过多，已截断）</span>。
      </p>
      <a-table row-key="key" size="small" :data="gapRows" :pagination="{ pageSize: 20 }">
        <template #columns>
          <a-table-column title="数据集" data-index="dataset_id" />
          <a-table-column title="频率" data-index="frequency" :width="70" />
          <a-table-column title="标的" data-index="subject_id" :width="130" />
          <a-table-column title="从" :width="180"
            ><template #cell="{ record }">{{ formatDateTime(record.from) }}</template></a-table-column
          >
          <a-table-column title="到（不含）" :width="180"
            ><template #cell="{ record }">{{ formatDateTime(record.to) }}</template></a-table-column
          >
          <a-table-column title="缺失根数" data-index="missing" :width="90" />
        </template>
      </a-table>
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import ActivityLog from "@/components/activity-log/index.vue";
import {
  cancelKlineBackfill,
  checkKline,
  getFetchPlans,
  listKlineBackfills,
  submitKlineBackfill,
  type CheckKlineResponse,
  type GetFetchPlansResponse,
  type KlineBackfillJob
} from "@/api/collector/fetch-plan";
import { useSpaceStore } from "@/store/modules/space";
import { createLatestRequestGuard } from "@/utils/latest-request";
import { activityTimeText } from "@/components/activity-log/activity-log";
import { backfillCancelable, backfillProgress, functionStateInfo, jobStatusInfo, shortHash, toRFC3339 } from "./fetch-plan-view";

const spaceStore = useSpaceStore();
const { selectedSpaceId } = storeToRefs(spaceStore);
const plansGuard = createLatestRequestGuard();
const jobsGuard = createLatestRequestGuard();

const plans = ref<GetFetchPlansResponse>({});
const plansLoading = ref(false);
const jobs = ref<KlineBackfillJob[]>([]);
const jobsLoading = ref(false);
const jobsPage = ref(1);
const jobsPageSize = 20;
const jobsTotal = ref(0);

const rangeVisible = ref(false);
const rangeMode = ref<"check" | "backfill">("check");
const rangeBusy = ref(false);
const range = ref({ dataset_id: "", subject_id: "", from: "", to: "", force: false, deep: false });
const gapsVisible = ref(false);
const gapResult = ref<CheckKlineResponse>({});

const formatDateTime = activityTimeText;
const planError = computed(() => (plans.value.open?.last_error || plans.value.applied?.last_error || "").trim());
const lastUpdated = computed(() => plans.value.open?.updated_at || plans.value.applied?.updated_at || "");
const datasetRows = computed(() =>
  (plans.value.datasets || []).map(item => ({ ...item, key: `${item.dataset_id}|${item.frequency}` }))
);
const gapRows = computed(() =>
  (gapResult.value.gaps || []).map((item, index) => ({ ...item, key: `${item.dataset_id}|${item.subject_id}|${item.from}|${index}` }))
);

async function loadPlans() {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) return;
  const request = plansGuard.begin();
  plansLoading.value = true;
  try {
    const data = await getFetchPlans(spaceId);
    if (request.isLatest() && spaceId === (selectedSpaceId.value || "")) plans.value = data;
  } catch (error) {
    if (!request.isLatest()) return;
    console.error("获取采集计划失败:", error);
  } finally {
    if (request.isLatest()) plansLoading.value = false;
  }
}

async function loadJobs() {
  const spaceId = selectedSpaceId.value || "";
  if (!spaceId) return;
  const request = jobsGuard.begin();
  jobsLoading.value = true;
  try {
    const data = await listKlineBackfills(spaceId, jobsPage.value, jobsPageSize);
    if (request.isLatest() && spaceId === (selectedSpaceId.value || "")) {
      jobs.value = data.items || [];
      jobsTotal.value = Number(data.page?.total || 0);
    }
  } catch (error) {
    if (!request.isLatest()) return;
    console.error("获取补采作业失败:", error);
  } finally {
    if (request.isLatest()) jobsLoading.value = false;
  }
}

function onJobsPageChange(page: number) {
  jobsPage.value = page;
  loadJobs();
}

function openRange(mode: "check" | "backfill") {
  rangeMode.value = mode;
  range.value = { dataset_id: "", subject_id: "", from: "", to: "", force: false, deep: false };
  rangeVisible.value = true;
}
const openCheck = () => openRange("check");
const openBackfill = () => openRange("backfill");

async function onRangeSubmit(): Promise<boolean> {
  const from = toRFC3339(range.value.from);
  const to = toRFC3339(range.value.to);
  if (!from || !to || from >= to) {
    Message.error("请填写有效的起止时间（RFC3339，起始早于结束）");
    return false;
  }
  const base = { space_id: selectedSpaceId.value || "", dataset_id: range.value.dataset_id.trim() || undefined, subject_id: range.value.subject_id.trim() || undefined, from, to };
  rangeBusy.value = true;
  try {
    if (rangeMode.value === "check") {
      gapResult.value = await checkKline({ ...base, deep: range.value.deep });
      gapsVisible.value = true;
    } else {
      const result = await submitKlineBackfill({ ...base, force: range.value.force });
      Message.success(result.job_id ? `已创建补采作业，共 ${result.chunks || 0} 个分块` : "范围内没有缺口");
      void loadJobs();
    }
    rangeVisible.value = false;
    return true;
  } catch (error) {
    console.error("检查或补采失败:", error);
    return false;
  } finally {
    rangeBusy.value = false;
  }
}

function onCancel(job: KlineBackfillJob) {
  Modal.confirm({
    title: "取消补采作业",
    content: "尚未开始的分块不再执行，运行中的分块会等其结束。",
    onOk: async () => {
      await cancelKlineBackfill(selectedSpaceId.value || "", job.job_id || "");
      Message.success("已取消");
      void loadJobs();
    }
  });
}

watch(selectedSpaceId, () => {
  plansGuard.invalidate();
  jobsGuard.invalidate();
  plans.value = {};
  jobs.value = [];
  jobsPage.value = 1;
  jobsTotal.value = 0;
  void loadPlans();
  void loadJobs();
});
onMounted(() => {
  void loadPlans();
  void loadJobs();
});
</script>

<style scoped>
.collector-instances {
  display: flex;
  flex-direction: column;
  gap: var(--moox-space-4);
}
.instances-block {
  min-width: 0;
}
.block-header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-2);
  margin-bottom: var(--moox-space-2);
}
.block-header h3 {
  margin: 0;
  font-size: 15px;
}
.sub-title {
  margin: var(--moox-space-3) 0 var(--moox-space-2);
  font-size: 13px;
}
.block-alert {
  margin-bottom: var(--moox-space-2);
}
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.mono-text {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  word-break: break-all;
}
.job-error {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
