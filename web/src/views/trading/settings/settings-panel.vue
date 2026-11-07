<template>
  <a-spin :loading="loading" class="settings-panel">
    <a-alert v-if="loadError" type="error">{{ loadError }}</a-alert>
    <a-tabs v-else default-active-key="order">
      <a-tab-pane key="order" title="下单设置">
        <order-settings-form :settings="account?.order_settings" :saving="savingOrder" @save="saveOrder" />
      </a-tab-pane>
      <a-tab-pane key="fund" title="资金设置">
        <fund-settings-form :settings="account?.fund_settings" :saving="savingFund" @save="saveFund" />
      </a-tab-pane>
    </a-tabs>
  </a-spin>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from "vue";
import { Message } from "@arco-design/web-vue";
import {
  getLogicalAccountSettings,
  updateLogicalAccountFundSettings,
  updateLogicalAccountOrderSettings,
  type FundSettings,
  type LogicalAccountWithSettings,
  type OrderSettings
} from "@/api/trade/settings";
import FundSettingsForm from "./fund-settings-form.vue";
import OrderSettingsForm from "./order-settings-form.vue";

const props = defineProps<{ logicalAccountId: string }>();

const account = ref<LogicalAccountWithSettings>();
const loading = ref(false);
const loadError = ref("");
const savingOrder = ref(false);
const savingFund = ref(false);

async function load() {
  loading.value = true;
  loadError.value = "";
  try {
    account.value = (await getLogicalAccountSettings(props.logicalAccountId)).logical_account;
  } catch (error) {
    loadError.value = error instanceof Error ? error.message : String(error);
  } finally {
    loading.value = false;
  }
}

async function saveOrder(settings: OrderSettings) {
  savingOrder.value = true;
  try {
    account.value = (await updateLogicalAccountOrderSettings(props.logicalAccountId, settings)).logical_account;
    Message.success("下单设置已保存，下一轮收敛起生效");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : String(error));
  } finally {
    savingOrder.value = false;
  }
}

async function saveFund(settings: FundSettings) {
  savingFund.value = true;
  try {
    account.value = (await updateLogicalAccountFundSettings(props.logicalAccountId, settings)).logical_account;
    Message.success("资金设置已保存");
  } catch (error) {
    Message.error(error instanceof Error ? error.message : String(error));
  } finally {
    savingFund.value = false;
  }
}

onMounted(load);
watch(() => props.logicalAccountId, load);
</script>
