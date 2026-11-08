<template>
  <a-drawer :visible="Boolean(task)" :width="'min(960px, 100vw)'" :footer="false" unmount-on-close @cancel="emit('close')">
    <template #title>
      <div v-if="task" class="title">
        <span>{{ name || task.dataset_id }}</span>
        <span class="sub mono">{{ task.dataset_id }}</span>
      </div>
    </template>
    <template v-if="task">
      <div class="head">
        <div>
          <span :class="`tone-${healthTone(task.health)}`">{{ healthLabel(task.health) }}</span>
          <span v-if="task.health_reason && task.health !== 'ok'" class="reason">{{ task.health_reason }}</span>
        </div>
        <a-space :size="4" wrap>
          <a-button size="small" :loading="busy[task.dataset_id || '']" @click="emit('run', task)">立即运行</a-button>
          <a-button size="small" @click="emit('edit', task)">编辑</a-button>
          <a-button size="small" @click="emit('backfill', task)">补拉区间</a-button>
          <a-button size="small" @click="emit('toggle', task)">{{ task.status === "paused" ? "启用" : "停用" }}</a-button>
          <a-button
            size="small"
            status="danger"
            :disabled="(task.partition_count || 0) > 0"
            :title="(task.partition_count || 0) > 0 ? '已有分区的任务不能删除，只能停用' : undefined"
            @click="emit('remove', task)"
            >删除</a-button
          >
        </a-space>
      </div>

      <a-descriptions :column="{ xs: 1, sm: 1, md: 2 }" size="small" bordered class="summary">
        <a-descriptions-item label="频率 / 分区"
          >{{ task.freq || "—" }} / {{ partitionUnitText(task.partition_unit) }}</a-descriptions-item
        >
        <a-descriptions-item label="拉取间隔">{{
          isDailyFreq(task.freq) ? "每天一次" : `每收盘 ${task.pull_every_bars || 1} 根`
        }}</a-descriptions-item>
        <a-descriptions-item label="下次拉取">{{ formatInstant(task.next_pull_at, zone) }}</a-descriptions-item>
        <a-descriptions-item label="最近拉取边界 / 最近复核">
          <template v-if="task.last_boundary || task.last_review">
            {{ formatInstant(task.last_boundary, zone) }} / {{ formatInstant(task.last_review, zone) }}
          </template>
          <template v-else>还没有处理过，下一次检查立即复核</template>
        </a-descriptions-item>
        <a-descriptions-item label="复核间隔">{{ intervalText(task.interval_seconds) }}</a-descriptions-item>
        <a-descriptions-item label="最近成功的复核">{{ formatInstant(task.last_success_at, zone) }}</a-descriptions-item>
        <a-descriptions-item label="定稿至">{{ formatInstant(task.final_until, zone) }}</a-descriptions-item>
        <a-descriptions-item label="回补起点">{{ formatInstant(task.backfill_from, zone) }}</a-descriptions-item>
        <a-descriptions-item v-if="task.consecutive_skips" label="连续跳过">{{ task.consecutive_skips }} 次</a-descriptions-item>
        <a-descriptions-item label="线上保留 / DataNode"
          >{{ formatRetention(task.retention) }} / {{ task.data_node_id || "—" }}</a-descriptions-item
        >
        <a-descriptions-item label="创建"
          >{{ task.creator || "—" }} · {{ formatInstant(task.created_at, zone) }}</a-descriptions-item
        >
      </a-descriptions>

      <div v-if="task.blocked_reason" class="blocked">
        <div class="blocked-title">分区登记被拒</div>
        <div class="pre">{{ task.blocked_reason }}</div>
        <div class="sub">
          这通常是暂存库丢失后用残缺数据重新导出。需要在因子机上核对原文件，再删掉 mgr 里对应的分区登记后重跑，见模块文档第 9 节。
        </div>
      </div>

      <div v-if="task.unrecoverable?.length" class="unrecoverable">
        <div class="unrecoverable-head">
          <span>无法补齐的区间（超出线上保留期）</span>
          <a-button size="mini" status="danger" @click="confirmClearUnrecoverable">清除这些记录</a-button>
        </div>
        <a-table :data="task.unrecoverable" :pagination="false" size="mini" :bordered="{ cell: true }">
          <template #columns>
            <a-table-column title="标的"
              ><template #cell="{ record }">{{ record.subject_id || "全部标的" }}</template></a-table-column
            >
            <a-table-column title="序列标签" data-index="series_tag" />
            <a-table-column title="起"
              ><template #cell="{ record }">{{ formatInstant(record.start_time, zone) }}</template></a-table-column
            >
            <a-table-column title="止"
              ><template #cell="{ record }">{{ formatInstant(record.end_time, zone) }}</template></a-table-column
            >
          </template>
        </a-table>
      </div>

      <a-tabs
        :active-key="query.tab"
        @change="(key: string | number) => emit('update:query', { tab: String(key) === 'partitions' ? 'partitions' : 'runs' })"
      >
        <a-tab-pane key="runs" title="运行记录">
          <TaskRuns
            v-if="query.tab === 'runs'"
            :dataset-id="task.dataset_id || ''"
            :query="query"
            :zone="zone"
            :now-ms="nowMs"
            :task-running="task.last_run?.status === 'running'"
            :remote-enabled="remoteEnabled"
            @update:query="patch => emit('update:query', patch)"
            @changed="emit('changed')"
          />
        </a-tab-pane>
        <a-tab-pane key="partitions" title="分区">
          <TaskPartitions
            v-if="query.tab === 'partitions'"
            :dataset-id="task.dataset_id || ''"
            :query="query"
            :zone="zone"
            :remote-enabled="remoteEnabled"
            @update:query="patch => emit('update:query', patch)"
          />
        </a-tab-pane>
      </a-tabs>
    </template>
  </a-drawer>
</template>

<script setup lang="ts">
import { Message, Modal } from "@arco-design/web-vue";
import { updateArchiveTask, type ArchiveTask } from "@/api/archive";
import { formatInstant } from "@/utils/time-zone";
import { formatRetention } from "@/utils/retention";
import { healthLabel, healthTone, intervalText, isDailyFreq, partitionUnitText, type ArchiveQuery } from "./archive-model";
import TaskPartitions from "./task-partitions.vue";
import TaskRuns from "./task-runs.vue";

/** 任务详情（?task=）：数据来自任务表那一行，轮询刷新任务表时一并更新；页签、筛选、页码都在 URL 里。 */
const props = defineProps<{
  task: ArchiveTask | undefined;
  name: string;
  query: ArchiveQuery;
  zone: string;
  nowMs: number;
  busy: Record<string, boolean>;
  remoteEnabled?: boolean;
}>();
const emit = defineEmits<{
  "update:query": [patch: Partial<ArchiveQuery>];
  close: [];
  run: [task: ArchiveTask];
  edit: [task: ArchiveTask];
  backfill: [task: ArchiveTask];
  toggle: [task: ArchiveTask];
  remove: [task: ArchiveTask];
  changed: [];
}>();

function confirmClearUnrecoverable() {
  const id = props.task?.dataset_id || "";
  Modal.confirm({
    title: "清除无法补齐的记录？",
    content: "清除后健康等级不再因此显示异常。这些区间的数据已经超出线上保留期，清除不会找回数据。",
    okText: "清除",
    okButtonProps: { status: "danger" },
    cancelText: "取消",
    onBeforeOk: async () => {
      try {
        await updateArchiveTask({ dataset_id: id, clear_unrecoverable: true });
      } catch {
        return false;
      }
      Message.success("已清除");
      emit("changed");
      return true;
    }
  });
}
</script>

<style scoped>
.title {
  display: flex;
  align-items: baseline;
  gap: 8px;
}

.head {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: var(--moox-space-3);
}

.reason {
  margin-left: 8px;
  color: var(--color-text-2);
}

.summary {
  margin-bottom: var(--moox-space-3);
}

.blocked {
  margin-bottom: var(--moox-space-3);
  padding: 8px 12px;
  border-left: 3px solid rgb(var(--red-6));
  background: var(--color-fill-1);
}

.blocked-title {
  color: rgb(var(--red-6));
  font-weight: 600;
}

.pre {
  white-space: pre-wrap;
  word-break: break-all;
}

.unrecoverable {
  margin-bottom: var(--moox-space-3);
}

.unrecoverable-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
}

.sub {
  color: var(--color-text-3);
  font-size: 12px;
}

.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
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
