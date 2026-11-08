<template>
  <SectionBlock title="人工操作记录" flush>
    <template #extra>
      <a-select v-model="actionType" allow-clear placeholder="全部类型" size="small" style="width: 120px" @change="search">
        <a-option v-for="item in operatorActionTypeOptions" :key="item.value" :value="item.value">{{ item.label }}</a-option>
      </a-select>
    </template>
    <a-alert v-if="error" type="warning" class="alert">{{ error }}</a-alert>
    <a-spin :loading="loading" class="actions-spin">
      <div v-for="item in actions" :key="item.action_id" class="action">
        <div class="action-head">
          <strong>{{ operatorActionTypeText(item.action_type) }}</strong>
          <StatusMark :tone="operatorActionTone(item.status)">{{ actionStateLabels[item.status] || item.status }}</StatusMark>
        </div>
        <div class="tx-muted">{{ formatInstant(item.created_at, displayZone) }} · {{ item.reason || "未填原因" }}</div>
        <div v-if="operatorActionHint(item)" class="tx-muted">{{ operatorActionHint(item) }}</div>
        <div v-if="item.last_error" class="tx-neg action-error">{{ item.last_error }}</div>
      </div>
      <p v-if="!actions.length && !error && !loading" class="empty">
        {{ actionType ? "这个类型还没有操作记录。" : "还没有操作记录。" }}
      </p>
    </a-spin>
    <div v-if="total > pageSize" class="pager">
      <a-pagination simple size="small" :current="page" :page-size="pageSize" :total="total" @change="changePage" />
    </div>
  </SectionBlock>
</template>

<script setup lang="ts">
import { inject, ref, watch } from "vue";
import { actionStateLabels, listOperatorActions } from "@/api/trade";
import type { OperatorAction, OperatorActionType } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";

import SectionBlock from "../shared/section-block.vue";
import StatusMark from "../shared/status-mark.vue";
import { detailContextKey } from "./context";
import {
  operatorActionHint,
  operatorActionTone,
  operatorActionTypeOptions,
  operatorActionTypeText
} from "./operator-actions-model";
import { formatInstant } from "@/utils/time-zone";
import { useDisplayZone } from "@/hooks/useDisplayZone";

// 交易页的时间按当前空间登记的时区显示
const displayZone = useDisplayZone();
const { logical, dataVersion } = inject(detailContextKey)!;

const actionType = ref<OperatorActionType>();
const page = ref(1);
const pageSize = 10;
const total = ref(0);
const actions = ref<OperatorAction[]>([]);
const loading = ref(false);
const error = ref("");
const requests = createLatestRequestGuard();

async function load() {
  const request = requests.begin();
  loading.value = true;
  error.value = "";
  try {
    const rsp = await listOperatorActions({
      logical_account_id: logical.value.logical_account_id,
      action_type: actionType.value || undefined,
      page: { page: page.value, size: pageSize }
    });
    if (!request.isLatest()) return;
    actions.value = rsp.actions || [];
    total.value = rsp.page_result?.total || 0;
  } catch (cause) {
    if (!request.isLatest()) return;
    actions.value = [];
    total.value = 0;
    error.value = cause instanceof Error ? cause.message : "操作记录读取失败";
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}

function search() {
  page.value = 1;
  void load();
}
function changePage(value: number) {
  page.value = value;
  void load();
}

watch(
  () => logical.value.logical_account_id,
  () => {
    actionType.value = undefined;
    search();
  },
  { immediate: true }
);
watch(dataVersion, () => void load());
</script>

<style src="../shared/accounts.css"></style>
<style scoped>
.alert {
  width: auto;
  margin: var(--moox-space-3) 16px 0;
}

.actions-spin {
  display: block;
}

.action {
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border-2);
  font-size: 13px;
}

.action:last-child {
  border-bottom: 0;
}

.action-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--moox-space-3);
}

.action-error {
  word-break: break-all;
}

.empty {
  margin: 0;
  padding: 12px 16px;
  color: var(--color-text-3);
  font-size: 13px;
}

.pager {
  display: flex;
  justify-content: flex-end;
  padding: 8px 16px;
  border-top: 1px solid var(--color-border-2);
}
</style>
