<template>
  <a-modal
    :visible="visible"
    title="同步云节点列表"
    :width="1120"
    :mask-closable="false"
    :ok-loading="importing"
    :ok-text="`导入 ${selectedCandidates.length} 个节点`"
    :ok-button-props="{ disabled: selectedCandidates.length === 0 || scanning || importing }"
    @cancel="close"
    @ok="importSelected"
  >
    <a-form-item label="云账户" required>
      <a-select v-model="accountId" placeholder="请选择云账户" :disabled="scanning || importing" allow-clear>
        <a-option v-for="account in accounts" :key="account.account_id" :value="account.account_id">
          {{ account.account_name || account.account_id }} ({{ account.provider }})
        </a-option>
      </a-select>
    </a-form-item>
    <div class="sync-toolbar">
      <a-button type="primary" :loading="scanning" :disabled="!accountId || importing" @click="scan">
        <template #icon><icon-search /></template>
        扫描全部地域
      </a-button>
      <a-tag v-if="candidates.length" color="blue">函数 {{ candidates.length }}</a-tag>
      <a-tag v-if="selectedCandidates.length" color="green">可导入 {{ selectedCandidates.length }}</a-tag>
      <span class="sync-hint">共扫描 MooX 支持的全部地域；不可导入函数仍会展示原因。</span>
    </div>
    <a-alert v-for="error in regionErrors" :key="error.region" type="warning" show-icon class="region-error">
      {{ error.region }}：{{ error.message }}
    </a-alert>
    <a-table
      row-key="key"
      :data="tableRows"
      :loading="scanning"
      :pagination="false"
      :scroll="{ y: 480 }"
      size="small"
      :bordered="{ cell: true }"
    >
      <template #columns>
        <a-table-column title="导入" :width="64" align="center">
          <template #cell="{ record }">
            <a-checkbox v-model="selectedKeys[record.key]" :disabled="!record.importable || importing" />
          </template>
        </a-table-column>
        <a-table-column title="地域" data-index="region" :width="120" />
        <a-table-column title="Namespace" data-index="namespace" :width="130" />
        <a-table-column title="函数名" data-index="function_name" :width="280" />
        <a-table-column title="状态" data-index="status" :width="100" />
        <a-table-column title="类型" data-index="function_type" :width="90" />
        <a-table-column title="节点状态" :width="110">
          <template #cell="{ record }">{{ stateLabel(record.import_state) }}</template>
        </a-table-column>
        <a-table-column title="说明" data-index="reason" min-width="260" />
        <a-table-column title="操作" :width="64" align="center">
          <template #cell="{ record }">
            <a-tooltip content="移除预览项">
              <a-button type="text" status="danger" size="mini" :disabled="importing" @click="remove(record.key)">
                <template #icon><icon-delete /></template>
              </a-button>
            </a-tooltip>
          </template>
        </a-table-column>
      </template>
    </a-table>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  importSCFFunctions,
  previewSCFFunctions,
  type SCFFunctionCandidate,
  type SCFRegionScanError
} from "@/api/cloud-node";
import type { CloudAccount } from "@/api/cloud-account";

const props = defineProps<{ visible: boolean; accounts: CloudAccount[] }>();
const emit = defineEmits<{ (event: "update:visible", value: boolean): void; (event: "refresh"): void }>();
const accountId = ref("");
const candidates = ref<SCFFunctionCandidate[]>([]);
const regionErrors = ref<SCFRegionScanError[]>([]);
const selectedKeys = ref<Record<string, boolean>>({});
const scanning = ref(false);
const importing = ref(false);
const functionKey = (item: SCFFunctionCandidate) => `${item.function.region}/${item.function.namespace}/${item.function.function_name}`;
const tableRows = computed(() => candidates.value.map(item => ({ ...item, ...item.function, key: functionKey(item) })));
const selectedCandidates = computed(() => candidates.value.filter(item => selectedKeys.value[functionKey(item)]));

watch(
  () => [props.visible, props.accounts] as const,
  ([visible, accounts]) => {
    if (!visible) return;
    if (!accounts.some(item => item.account_id === accountId.value)) accountId.value = accounts.length === 1 ? accounts[0].account_id : "";
    candidates.value = [];
    regionErrors.value = [];
    selectedKeys.value = {};
  },
  { deep: true }
);

watch(accountId, (next, previous) => {
  if (!previous || next === previous) return;
  candidates.value = [];
  regionErrors.value = [];
  selectedKeys.value = {};
});

const close = () => emit("update:visible", false);
const stateLabel = (state: string | number) => {
  const labels: Record<string, string> = {
    SCF_FUNCTION_IMPORT_STATE_NEW: "新节点",
    SCF_FUNCTION_IMPORT_STATE_EXISTING: "已存在",
    SCF_FUNCTION_IMPORT_STATE_DELETED: "可恢复",
    SCF_FUNCTION_IMPORT_STATE_BLOCKED: "不可导入",
    "1": "新节点",
    "2": "已存在",
    "3": "可恢复",
    "4": "不可导入"
  };
  return labels[String(state)] || "未知";
};
const scan = async () => {
  if (!accountId.value) return;
  scanning.value = true;
  try {
    const response = await previewSCFFunctions(accountId.value);
    candidates.value = response.functions;
    regionErrors.value = response.region_errors;
    selectedKeys.value = Object.fromEntries(response.functions.filter(item => item.importable).map(item => [functionKey(item), true]));
    if (!response.functions.length && !response.region_errors.length) Message.info("未发现云函数");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "扫描云函数失败");
  } finally {
    scanning.value = false;
  }
};
const remove = (key: string) => {
  candidates.value = candidates.value.filter(item => functionKey(item) !== key);
  delete selectedKeys.value[key];
};
const importSelected = async () => {
  if (!accountId.value || selectedCandidates.value.length === 0) return;
  importing.value = true;
  try {
    const response = await importSCFFunctions(
      accountId.value,
      selectedCandidates.value.map(item => item.function)
    );
    const summary = `同步完成：新增 ${response.created}，恢复 ${response.restored}，已存在 ${response.unchanged}，失败 ${response.failed}`;
    if (response.failed > 0) {
      Message.warning(summary);
      const failures = new Map(response.results.filter(item => item.error_message).map(item => [`${item.function.region}/${item.function.namespace}/${item.function.function_name}`, item.error_message]));
      candidates.value = candidates.value
        .filter(item => failures.has(functionKey(item)))
        .map(item => ({ ...item, reason: failures.get(functionKey(item)) || item.reason }));
      selectedKeys.value = Object.fromEntries(candidates.value.map(item => [functionKey(item), true]));
      emit("refresh");
    } else {
      Message.success(summary);
      emit("refresh");
      close();
    }
  } catch (error) {
    Message.error(error instanceof Error ? error.message : "导入云节点失败");
  } finally {
    importing.value = false;
  }
};
</script>

<style scoped>
.sync-toolbar { display: flex; align-items: center; gap: var(--moox-space-3); margin-bottom: var(--moox-space-3); flex-wrap: wrap; }
.sync-hint { color: var(--color-text-3); font-size: 12px; }
.region-error { margin-bottom: var(--moox-space-2); }
</style>
