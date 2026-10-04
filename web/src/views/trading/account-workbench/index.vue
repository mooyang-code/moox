<template>
  <div class="moox-page trading-account-workbench">
    <div class="moox-inner">
      <PageTitleTabs :model-value="activeView" :items="tabs" aria-label="账户工作台" @change="syncRoute" />
      <section class="trading-account-content">
        <AccountOverview v-if="activeView === 'trading'" :embedded="true" />
        <LogicalAccounts v-else :embedded="true" />
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from "vue";
import { useRoute, useRouter } from "vue-router";
import PageTitleTabs from "@/components/page-title-tabs/index.vue";
import AccountOverview from "../account-overview/account-overview.vue";
import LogicalAccounts from "../logical-accounts/index.vue";

defineOptions({ name: "trading-account-workbench" });

type WorkbenchView = "trading" | "strategy";

const tabs = [
  { key: "trading", label: "执行账户" },
  { key: "strategy", label: "组合账户" }
] as const;

const route = useRoute();
const router = useRouter();
const activeView = computed<WorkbenchView>(() => viewFromQuery(route.query.view));

function viewFromQuery(value: unknown): WorkbenchView {
  return value === "strategy" ? "strategy" : "trading";
}

function syncRoute(value: string) {
  const view = viewFromQuery(value);
  const query = {
    ...route.query,
    view: view === "strategy" ? "strategy" : undefined,
    mode: view === "strategy" ? undefined : route.query.mode,
    logical_account_id: view === "trading" ? undefined : route.query.logical_account_id
  };
  void router.replace({ path: "/trading/accounts", query });
}
</script>

<style scoped>
.trading-account-workbench {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
}

.trading-account-workbench > .moox-inner {
  display: flex;
  height: 100%;
  min-height: 0;
  flex-direction: column;
}

.trading-account-content {
  min-height: 0;
  flex: 1;
  margin-top: var(--moox-space-3);
  overflow: hidden;
}

.trading-account-content :deep(.moox-page) {
  height: 100%;
  min-height: 0;
  padding: 0;
  overflow: auto;
  background: transparent;
}

.trading-account-content :deep(.moox-page > .moox-inner) {
  min-height: 0;
  padding: 0;
  border: 0;
  border-radius: 0;
  box-shadow: none;
}
</style>
