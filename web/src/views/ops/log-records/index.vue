<template>
  <div class="moox-page">
    <div class="moox-inner logs-page">
      <header class="logs-head">
        <div>
          <h2>重要日志</h2>
          <p class="muted">
            各服务的错误日志和关键事件（进程启动、暂停与恢复等），保留 30 天。普通日志在本机文件或 CLS
            里查。带关键字查询时只看最近 7 天。
          </p>
        </div>
        <a-button :loading="loading" @click="reload">刷新</a-button>
      </header>

      <form class="logs-filters" aria-label="筛选" @submit.prevent="reload">
        <a-input v-model="filters.component" aria-label="组件" placeholder="组件，如 storage-node" allow-clear />
        <a-input v-model="filters.host_id" aria-label="主机" placeholder="主机 ID，如 storage" allow-clear />
        <a-select v-model="filters.level" aria-label="级别">
          <a-option value="">错误和关键事件</a-option>
          <a-option value="error">只看错误</a-option>
        </a-select>
        <a-input v-model="filters.keyword" aria-label="关键字" placeholder="在正文、事件名、代码位置里查找" allow-clear />
        <a-button html-type="submit" type="primary">查询</a-button>
      </form>

      <a-alert v-if="error" type="error" class="logs-error">{{ error }}</a-alert>

      <div class="logs-table-wrap">
        <table class="logs-table" aria-label="重要日志">
          <colgroup>
            <col style="width: 200px" />
            <col style="width: 72px" />
            <col style="width: 96px" />
            <col style="width: 150px" />
            <col />
          </colgroup>
          <thead>
            <tr>
              <th>时间</th>
              <th>级别</th>
              <th>主机</th>
              <th>组件</th>
              <th>内容</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="record in records"
              :key="record.id"
              class="logs-row"
              tabindex="0"
              @click="open(record)"
              @keydown.enter="open(record)"
            >
              <td>{{ formatTime(record.logged_at) }}</td>
              <td>
                <span class="logs-level" :class="`is-${levelTone(record)}`">{{ levelLabel(record) }}</span>
              </td>
              <td>{{ record.host_id || "--" }}</td>
              <td>{{ record.component || "--" }}</td>
              <td class="logs-message">
                <span v-if="record.event" class="logs-event">{{ record.event }}</span>
                {{ firstLine(record.message) }}
                <span v-if="mergedCount(record)" class="muted">（此前同一位置另有 {{ mergedCount(record) }} 条被合并）</span>
              </td>
            </tr>
            <tr v-if="!records.length">
              <td colspan="5" class="logs-empty">{{ loading ? "加载中…" : "没有符合条件的日志" }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="hasMore" class="logs-more">
        <a-button :loading="loadingMore" @click="loadMore">加载更早的日志</a-button>
      </div>
    </div>

    <a-drawer v-model:visible="detailVisible" title="日志详情" width="min(720px, 100vw)" :footer="false">
      <dl v-if="selected" class="logs-detail">
        <dt>时间</dt>
        <dd>{{ formatTime(selected.logged_at) }}</dd>
        <dt>级别</dt>
        <dd>{{ levelLabel(selected) }}</dd>
        <dt>主机 / 组件</dt>
        <dd>{{ selected.host_id || "--" }} / {{ selected.component || "--" }}</dd>
        <template v-if="selected.event">
          <dt>事件</dt>
          <dd>{{ selected.event }}</dd>
        </template>
        <dt>代码位置</dt>
        <dd class="logs-mono">{{ selected.caller || "--" }}</dd>
        <template v-if="mergedCount(selected)">
          <dt>合并</dt>
          <dd>在这条之前，同一代码位置另有 {{ mergedCount(selected) }} 条日志因为太密没有保存。</dd>
        </template>
        <dt>内容</dt>
        <dd>
          <pre class="logs-pre">{{ selected.message }}</pre>
        </dd>
        <template v-if="fieldEntries(selected).length">
          <dt>字段</dt>
          <dd>
            <table class="logs-fields">
              <tbody>
                <tr v-for="[key, value] in fieldEntries(selected)" :key="key">
                  <th>{{ key }}</th>
                  <td>{{ value }}</td>
                </tr>
              </tbody>
            </table>
          </dd>
        </template>
      </dl>
    </a-drawer>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from "vue";
import { monitorApi, type LogRecord } from "@/api/monitor";
import { useDisplayZone } from "@/hooks/useDisplayZone";
import { formatInstant } from "@/utils/time-zone";
import { fieldEntries, firstLine, levelLabel, levelTone, mergedCount } from "./logs-model";

const PAGE_SIZE = 100;

// 时间和运维下的其他页面（监控告警、部署）一样按 UTC 显示并带时区后缀，同一个故障在几个页面上的时间能对上。
const displayZone = useDisplayZone("global");
const filters = reactive({ component: "", host_id: "", level: "", keyword: "" });
// applied 是上一次“查询”实际用的条件：“加载更早”沿用它，输入框里改了但还没点查询的内容不参与，免得两种条件的记录混在一个列表里。
let applied = { component: "", host_id: "", level: "", keyword: "" };
const records = ref<LogRecord[]>([]);
const hasMore = ref(false);
const loading = ref(false);
const loadingMore = ref(false);
const error = ref("");
const detailVisible = ref(false);
const selected = ref<LogRecord | null>(null);
// 每次查询递增：较早发出的请求晚到时不覆盖新的结果。
let generation = 0;

type Conditions = typeof applied;

function request(conditions: Conditions, beforeId?: number | string) {
  return monitorApi.queryLogRecords({
    ...conditions,
    limit: PAGE_SIZE,
    ...(beforeId ? { before_id: beforeId } : {})
  });
}

async function reload() {
  const next: Conditions = {
    component: filters.component.trim(),
    host_id: filters.host_id.trim(),
    level: filters.level,
    keyword: filters.keyword.trim()
  };
  const current = ++generation;
  loading.value = true;
  try {
    const rsp = await request(next);
    if (current !== generation) return;
    // 查询成功才换条件：失败时列表还是旧的，“加载更早”要继续用旧条件。
    applied = next;
    records.value = rsp.records ?? [];
    hasMore.value = rsp.has_more === true;
    error.value = "";
  } catch (err) {
    if (current === generation) error.value = err instanceof Error ? err.message : "重要日志加载失败";
  } finally {
    if (current === generation) loading.value = false;
  }
}

async function loadMore() {
  const last = records.value[records.value.length - 1];
  if (!last?.id || loading.value || loadingMore.value) return;
  const current = generation;
  loadingMore.value = true;
  try {
    const rsp = await request(applied, last.id);
    if (current !== generation) return;
    records.value = [...records.value, ...(rsp.records ?? [])];
    hasMore.value = rsp.has_more === true;
    error.value = "";
  } catch (err) {
    if (current === generation) error.value = err instanceof Error ? err.message : "重要日志加载失败";
  } finally {
    loadingMore.value = false;
  }
}

function open(record: LogRecord) {
  selected.value = record;
  detailVisible.value = true;
}

function formatTime(value?: string) {
  return formatInstant(value, displayZone.value);
}

onMounted(reload);
</script>

<style scoped lang="scss">
.logs-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.logs-head h2 {
  margin: 0 0 4px;
  font-size: 18px;
  font-weight: 600;
}
.logs-head p {
  margin: 0;
  font-size: 13px;
}
.muted {
  color: var(--color-text-3);
}
.logs-filters {
  display: grid;
  grid-template-columns: 200px 160px 160px minmax(0, 1fr) auto;
  gap: 8px;
  margin-bottom: 12px;
}
@media (max-width: 900px) {
  .logs-filters {
    grid-template-columns: minmax(0, 1fr);
  }
}
.logs-error {
  margin-bottom: 12px;
}
.logs-table-wrap {
  overflow-x: auto;
  border: 1px solid var(--color-border-2);
  border-radius: 6px;
}
.logs-table {
  width: 100%;
  min-width: 760px;
  border-collapse: collapse;
  table-layout: fixed;
  font-size: 13px;
}
.logs-table th,
.logs-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border-1);
  text-align: left;
  vertical-align: top;
}
.logs-table th {
  color: var(--color-text-2);
  font-weight: 500;
  background: var(--color-fill-1);
}
.logs-row {
  cursor: pointer;
}
.logs-row:hover,
.logs-row:focus-visible {
  background: var(--color-fill-1);
  outline: none;
}
.logs-message {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.logs-event {
  margin-right: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  color: var(--color-text-2);
}
.logs-level.is-error {
  color: rgb(var(--danger-6));
}
.logs-level.is-event {
  color: var(--color-text-2);
}
.logs-empty {
  color: var(--color-text-3);
  text-align: center;
}
.logs-more {
  margin-top: 12px;
  text-align: center;
}
.logs-detail {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  gap: 10px 12px;
  margin: 0;
  font-size: 13px;
}
.logs-detail dt {
  color: var(--color-text-3);
}
.logs-detail dd {
  margin: 0;
  min-width: 0;
}
.logs-mono,
.logs-pre {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
}
.logs-pre {
  margin: 0;
  padding: 10px;
  max-height: 50vh;
  overflow: auto;
  border-radius: 6px;
  background: var(--color-fill-1);
  white-space: pre-wrap;
  word-break: break-all;
}
.logs-fields {
  width: 100%;
  border-collapse: collapse;
}
.logs-fields th,
.logs-fields td {
  padding: 4px 8px 4px 0;
  text-align: left;
  vertical-align: top;
  word-break: break-all;
}
.logs-fields th {
  width: 160px;
  color: var(--color-text-2);
  font-weight: 400;
}
</style>
