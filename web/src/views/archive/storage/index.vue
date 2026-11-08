<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>归档存储</h2>
      </div>

      <div v-if="!loaded && !error" class="muted">读取中…</div>
      <div v-else-if="!loaded && error" class="is-bad">
        无法读取归档服务：{{ error }}<template v-if="unavailableHint">。{{ unavailableHint }}</template> ·
        <a-link @click="retry">重试</a-link>
      </div>
      <template v-else-if="overview">
        <div v-if="error" class="is-bad stale">
          读取失败 · 上次成功 {{ lastOkAt ? formatInstant(lastOkAt, "UTC", "clock") : "—" }} ·
          <a-link @click="retry">重试</a-link>
        </div>

        <section class="block" aria-label="归档执行器">
          <h3>执行器</h3>
          <div v-if="!worker" class="is-warn">
            还没有归档执行器登记。在因子机部署 archive-worker：<code
              >moox-cli setup deploy-unit --host &lt;主机&gt; --profile archive-worker</code
            >
          </div>
          <a-descriptions v-else :column="{ xs: 1, sm: 1, md: 2 }" size="small" bordered>
            <a-descriptions-item label="状态">
              <span v-if="worker.active" class="is-ok">在线</span>
              <span v-else class="is-bad">离线</span>
              <span v-if="worker.maintaining" class="is-warn"> · 维护中</span>
            </a-descriptions-item>
            <a-descriptions-item label="所在主机">
              <template v-if="hostError">读取主机失败：{{ hostError }} · <a-link @click="loadHost">重试</a-link></template>
              <template v-else>{{ workerHostText(host) || "读取中…" }}</template>
            </a-descriptions-item>
            <a-descriptions-item label="归档根目录">
              <span class="mono">{{ worker.root_dir || "—" }}</span>
            </a-descriptions-item>
            <a-descriptions-item label="磁盘">
              <template v-if="disk">
                <span :title="disk.title">{{ disk.text }}</span>
                <div v-if="disk.insufficient" class="is-bad">{{ disk.warning }}</div>
              </template>
              <template v-else>—</template>
            </a-descriptions-item>
            <a-descriptions-item label="暂存库">{{ formatBytes(toNum(worker.staging_bytes)) }}</a-descriptions-item>
            <a-descriptions-item label="最近心跳">
              {{ formatInstant(worker.last_heartbeat_at, "UTC") }}（{{ relativeTime(worker.last_heartbeat_at, nowMs) }}）
            </a-descriptions-item>
            <a-descriptions-item label="执行器 ID">
              <span class="mono">{{ worker.worker_id || "—" }}</span>
            </a-descriptions-item>
            <a-descriptions-item label="版本 / 实例">
              <span class="mono">{{ worker.version || "—" }} / {{ worker.instance_id || "—" }}</span>
            </a-descriptions-item>
          </a-descriptions>
          <div v-if="staleWorkers.length" class="note">
            另有 {{ staleWorkers.length }} 个过期的执行器登记，确认不再使用后可以退役：<code
              >moox-cli archive worker retire --worker &lt;ID&gt; --yes</code
            >
            <div v-for="item in staleWorkers" :key="item.worker_id" class="muted mono">
              {{ item.worker_id }} · 最后心跳 {{ formatInstant(item.last_heartbeat_at, "UTC") }}
            </div>
          </div>
        </section>

        <section class="block" aria-label="对象存储">
          <h3>对象存储</h3>
          <div v-if="!store?.endpoint" class="muted">没有配置对象存储：分区文件只在执行器本机磁盘上，不能在页面下载。</div>
          <template v-else>
            <a-descriptions :column="{ xs: 1, sm: 1, md: 2 }" size="small" bordered>
              <a-descriptions-item label="类型">{{ builtin ? "内置 SeaweedFS" : "外部 S3 兼容存储" }}</a-descriptions-item>
              <a-descriptions-item label="地址"
                ><span class="mono">{{ store.endpoint }}</span></a-descriptions-item
              >
              <a-descriptions-item label="桶"
                ><span class="mono">{{ store.bucket || "—" }}</span></a-descriptions-item
              >
              <a-descriptions-item label="前缀"
                ><span class="mono">{{ store.prefix || "—" }}</span></a-descriptions-item
              >
              <a-descriptions-item label="存储类型">{{ storageClassText(store) }}</a-descriptions-item>
              <a-descriptions-item label="探测">
                <span v-if="!store.probe_error" class="is-ok">正常</span>
                <template v-else>
                  <span class="is-bad">失败：{{ store.probe_error }}</span>
                  <div v-if="store.unavailable_since" class="muted">
                    自 {{ formatInstant(store.unavailable_since, "UTC") }} 起连不上
                  </div>
                </template>
              </a-descriptions-item>
            </a-descriptions>
            <div v-if="builtin" class="note muted">
              内置 SeaweedFS 和归档在同一台机器、同一块盘上，能防误删和文件损坏，不防整盘故障。
            </div>
          </template>
        </section>

        <section class="block" aria-label="避让时段">
          <h3>避让时段</h3>
          <div class="muted intro">
            执行器读取 Storage 时避开采集函数的触发时刻；规则由 archive-mgr 按有归档任务的空间里生效的采集频率推出，每 10
            分钟重算。
          </div>
          <a-descriptions v-if="avoidance" :column="{ xs: 1, sm: 1, md: 2 }" size="small" bordered>
            <a-descriptions-item label="每分钟">{{ minuteWindowText(avoidance) }}</a-descriptions-item>
            <a-descriptions-item label="触发间隔">
              {{
                avoidance.every_minutes
                  ? `每 ${avoidance.every_minutes} 分钟（分钟数为它的倍数的第 0 秒）`
                  : "没有低于 1 小时的采集频率"
              }}
            </a-descriptions-item>
            <a-descriptions-item label="整点">{{ hourQuietText(avoidance) }}</a-descriptions-item>
            <a-descriptions-item label="计算时间">
              {{ avoidance.computed_at ? formatInstant(avoidance.computed_at, "UTC") : "还在用启动默认值" }}
            </a-descriptions-item>
            <a-descriptions-item label="依据" :span="2">{{ avoidanceBasisText(avoidance) }}</a-descriptions-item>
          </a-descriptions>
          <div v-else class="muted">archive-mgr 没有返回避让规则。</div>
        </section>

        <section class="block" aria-label="目录对照表">
          <h3>目录对照表</h3>
          <div class="text">
            分区文件在
            <span class="mono">{{ rootDir }}/&lt;空间&gt;/&lt;用途&gt;/&lt;目录名&gt;/&lt;年&gt;/</span>
            下，用途是 collect（采集结果）、factor（因子结果）或 data（其他）。根目录下的
            <span class="mono">datasets.csv</span>
            列出每个目录对应的数据集（列：space_id、purpose、dir_name、dataset_id、name、freq），每次写数据集说明后刷新；它只在执行器本机，不上传对象存储。
          </div>
        </section>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { getArchiveOverview, type GetArchiveOverviewRsp } from "@/api/archive";
import { StaleSpaceRequestError } from "@/api/admin/stale-space";
import { formatBytes } from "@/api/modules/host-monitor";
import { usePolling } from "@/hooks/usePolling";
import { useSpaceStore } from "@/store/modules/space";
import { RequestGate } from "@/utils/request-gate";
import { formatInstant } from "@/utils/time-zone";
import { relativeTime } from "@/views/home/home-model";
import {
  archiveUnavailableHint,
  avoidanceBasisText,
  diskStatus,
  hourQuietText,
  isBuiltinObjectStore,
  minuteWindowText,
  storageClassText,
  toNum,
  workerHostText
} from "../shared/archive-model";
import { useArchiveWorkerHost } from "../shared/use-archive-worker-host";

/**
 * 数据归档 › 归档存储：归档放在哪（执行器所在主机、根目录、对象存储）、磁盘用量、避让时段与目录对照表。
 * 执行器与空间无关，时间按 UTC；概览接口要带空间请求头，所以等拿到空间再读，30 秒轮询。
 */
const spaceStore = useSpaceStore();
const spaceId = computed(() => spaceStore.selectedSpaceId || "");

const overview = ref<GetArchiveOverviewRsp | null>(null);
const loaded = ref(false);
const error = ref("");
const lastOkAt = ref<number | null>(null);
const nowMs = ref(Date.now());
const gate = new RequestGate();

async function load() {
  if (!spaceId.value) return;
  const token = gate.next();
  try {
    const rsp = await getArchiveOverview({ silentError: true });
    if (!gate.isCurrent(token)) return;
    overview.value = rsp;
    loaded.value = true;
    error.value = "";
    lastOkAt.value = Date.now();
  } catch (cause) {
    if (cause instanceof StaleSpaceRequestError || !gate.isCurrent(token)) return;
    error.value = cause instanceof Error && cause.message ? cause.message : "请稍后重试";
  } finally {
    if (gate.isCurrent(token)) nowMs.value = Date.now();
  }
}

usePolling(load, 30_000, () => Boolean(spaceId.value));
watch(spaceId, () => void load(), { immediate: true });

const { host, error: hostError, load: loadHost } = useArchiveWorkerHost();
void loadHost();

function retry() {
  void load();
  if (!host.value) void loadHost();
}

const worker = computed(() => overview.value?.worker);
const staleWorkers = computed(() => overview.value?.stale_workers || []);
const store = computed(() => worker.value?.object_store);
const builtin = computed(() => isBuiltinObjectStore(store.value?.endpoint));
const disk = computed(() => diskStatus(worker.value));
const avoidance = computed(() => overview.value?.avoidance);
const rootDir = computed(() => worker.value?.root_dir || "<归档根目录>");
const unavailableHint = computed(() => archiveUnavailableHint(error.value));
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--moox-space-2);
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.block {
  margin-bottom: var(--moox-space-4);
}

h3 {
  margin: 0 0 8px;
  font-size: 15px;
  font-weight: 600;
}

.note {
  margin-top: 8px;
  line-height: 22px;
}

.intro {
  margin-bottom: 8px;
}

.text {
  max-width: 880px;
  line-height: 24px;
}

.stale {
  margin-bottom: var(--moox-space-2);
}

.muted {
  color: var(--color-text-3);
}

.mono,
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

code {
  font-size: 12px;
}

.is-ok {
  color: rgb(var(--green-6));
}

.is-warn {
  color: rgb(var(--orange-6));
}

.is-bad {
  color: rgb(var(--red-6));
}
</style>
