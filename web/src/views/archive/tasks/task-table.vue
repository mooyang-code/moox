<template>
  <section aria-label="归档任务">
    <div v-if="!loaded && error" class="load-error">
      无法读取归档任务：{{ error }} · <a-link @click="emit('retry')">重试</a-link>
    </div>
    <a-table
      v-else
      row-key="dataset_id"
      size="small"
      :bordered="{ cell: true }"
      :loading="!loaded"
      :data="tasks"
      :pagination="false"
      :scroll="{ x: 1280 }"
    >
      <template #empty>
        <div class="empty-text">{{ emptyText }}</div>
      </template>
      <template #columns>
        <a-table-column title="数据集" :width="220">
          <template #cell="{ record }">
            <a-link @click="emit('open', record.dataset_id)">{{ names[record.dataset_id] || record.dataset_id }}</a-link>
            <div class="sub mono">{{ record.dataset_id }}</div>
          </template>
        </a-table-column>
        <a-table-column title="频率 / 线上保留" :width="130">
          <template #cell="{ record }">{{ record.freq || "—" }} / {{ formatRetention(record.retention) }}</template>
        </a-table-column>
        <a-table-column title="拉取" :width="250">
          <template #cell="{ record }">
            <div :title="record.next_pull_at ? `下次拉取 ${formatInstant(record.next_pull_at, zone)}` : undefined">
              {{ pullText(record, zone) }}
            </div>
            <div class="sub">{{ reviewText(record) }}</div>
          </template>
        </a-table-column>
        <a-table-column title="已归档范围" :width="260">
          <template #cell="{ record }">
            <template v-if="record.min_data_time">
              <div :title="`${formatInstant(record.min_data_time, zone)} ～ ${formatInstant(record.max_data_time, zone)}`">
                {{ formatInstant(record.min_data_time, zone, "minute") }} ～
                {{ formatInstant(record.max_data_time, zone, "minute") }}
              </div>
              <div class="sub">
                {{ record.partition_count || 0 }} 个分区 · 文件 {{ formatBytes(toNum(record.total_file_bytes))
                }}<template v-if="remoteEnabled">
                  · 远端 {{ record.remote_verified_count || 0 }}/{{ record.file_count || 0 }}</template
                >
              </div>
            </template>
            <span v-else class="sub">还没有分区</span>
          </template>
        </a-table-column>
        <a-table-column title="最近运行" :width="250">
          <template #cell="{ record }">
            <template v-if="record.last_run">
              <div>
                {{ kindLabel(record.last_run.kind) }}
                <span :class="`tone-${runTone(record.last_run.status)}`">{{ runStatusLabel(record.last_run.status) }}</span>
                <span class="sub"
                  >{{ formatInstant(record.last_run.started_at || record.last_run.scheduled_at, zone, "minute") }} ·
                  {{ runDurationText(record.last_run, nowMs) }}</span
                >
              </div>
              <div v-if="record.last_run.status === 'running'" class="sub">已读 {{ toNum(record.last_run.rows_read) }} 行</div>
              <div v-else-if="record.last_run.status === 'failed'" class="sub ellipsis" :title="record.last_run.message">
                {{ record.last_run.message || "没有失败原因" }}
              </div>
            </template>
            <span v-else class="sub">还没有运行</span>
          </template>
        </a-table-column>
        <a-table-column title="健康" :width="220">
          <template #cell="{ record }">
            <span :class="`tone-${healthTone(record.health)}`">{{ healthLabel(record.health) }}</span>
            <div v-if="record.health_reason && record.health !== 'ok'" class="reason">{{ record.health_reason }}</div>
          </template>
        </a-table-column>
        <a-table-column title="操作" :width="190" fixed="right">
          <template #cell="{ record }">
            <a-space :size="4">
              <a-button
                size="mini"
                :loading="busy[record.dataset_id]"
                title="读法同复核；排队中的到点拉取会被取消、让位给它"
                @click="emit('run', record)"
                >立即运行</a-button
              >
              <a-button size="mini" @click="emit('edit', record)">编辑</a-button>
              <a-dropdown trigger="click" @select="(value: unknown) => onMore(String(value), record)">
                <a-button size="mini">更多</a-button>
                <template #content>
                  <a-doption value="backfill">补拉区间</a-doption>
                  <a-doption value="toggle">{{ record.status === "paused" ? "启用" : "停用" }}</a-doption>
                  <a-doption
                    value="remove"
                    :disabled="(record.partition_count || 0) > 0"
                    :title="(record.partition_count || 0) > 0 ? DELETE_BLOCKED : undefined"
                    >删除</a-doption
                  >
                </template>
              </a-dropdown>
            </a-space>
          </template>
        </a-table-column>
      </template>
    </a-table>
  </section>
</template>

<script setup lang="ts">
import { computed } from "vue";
import type { ArchiveTask } from "@/api/archive";
import { formatBytes } from "@/api/modules/host-monitor";
import { formatInstant } from "@/utils/time-zone";
import { formatRetention } from "@/utils/retention";
import {
  healthLabel,
  healthTone,
  kindLabel,
  pullText,
  reviewText,
  runDurationText,
  runStatusLabel,
  runTone,
  toNum
} from "../shared/archive-model";

/**
 * 任务表。表格放不下的三列（下次拉取、已归档范围、最近运行）到分钟，悬停看到秒；健康原因直接写在单元格里。
 * 已有分区的任务不能删除，只能停用。
 */
const DELETE_BLOCKED = "已有分区的任务不能删除，只能停用";

const props = defineProps<{
  tasks: ArchiveTask[];
  loaded: boolean;
  error: string;
  unarchivedCount: number;
  /** 数据集读到了没有：没读到时空态不能说“没有需要归档的数据集” */
  datasetsLoaded: boolean;
  names: Record<string, string>;
  zone: string;
  nowMs: number;
  busy: Record<string, boolean>;
  /** 批 2 之后：worker 启用了对象存储时显示远端副本 */
  remoteEnabled?: boolean;
}>();
const emit = defineEmits<{
  open: [datasetId: string];
  run: [task: ArchiveTask];
  edit: [task: ArchiveTask];
  backfill: [task: ArchiveTask];
  toggle: [task: ArchiveTask];
  remove: [task: ArchiveTask];
  retry: [];
}>();

const emptyText = computed(() =>
  !props.datasetsLoaded
    ? "还没有归档任务"
    : props.unarchivedCount
      ? "还没有归档任务，从上面的未归档数据集开始建"
      : "这个空间没有需要归档的时序数据集"
);

function onMore(action: string, task: ArchiveTask) {
  if (action === "backfill") emit("backfill", task);
  else if (action === "toggle") emit("toggle", task);
  else if (action === "remove" && !(task.partition_count || 0)) emit("remove", task);
}
</script>

<style scoped>
.sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

.ellipsis {
  max-width: 230px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.reason {
  margin-top: 2px;
  color: var(--color-text-2);
  font-size: 12px;
  line-height: 18px;
  white-space: normal;
}

.load-error {
  padding: var(--moox-space-4) 0;
  color: rgb(var(--red-6));
}

.empty-text {
  padding: var(--moox-space-4) 0;
  color: var(--color-text-3);
}

.tone-ok {
  color: rgb(var(--green-6));
}

.tone-warn {
  color: rgb(var(--orange-6));
}

.tone-bad {
  color: rgb(var(--red-6));
}

.tone-muted {
  color: var(--color-text-3);
}
</style>
