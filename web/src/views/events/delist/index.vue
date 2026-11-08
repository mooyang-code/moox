<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>下架处理</h2>
        <a-button type="primary" @click="delistVisible = true">手工登记下架</a-button>
      </div>

      <section class="delist-text">
        <h4>下架怎么被发现</h4>
        <p>
          Collector 定时检查交易所公布的下架：币安现货读下架计划（要有币安只读 Key），币安永续和 OKX
          永续读合约信息里的下架时间。检测漏掉的，或者想做演练，可以在这里手工登记。
        </p>
        <h4>系统怎么处理</h4>
        <p>
          发现后立即禁止策略买入这个标的；下架前 24 小时平掉策略持有的部分，下架前 2
          小时复查一次，下架后再核对。每一步都记在处理记录里，失败时会告警。
        </p>
      </section>

      <p class="delist-rule">
        <span>{{ systemRuleLine }}</span>
        <a-link href="#/events/subscriptions">到事件订阅页修改</a-link>
      </p>

      <DelistDialog
        v-model:visible="delistVisible"
        :system-action-enabled="systemActionEnabled"
        :system-rule-enabled="systemRuleEnabled"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import DelistDialog from "../shared/delist-dialog.vue";
import { useEventCatalog } from "../shared/use-event-catalog";

defineOptions({ name: "EventDelist" });

const { systemActionEnabled, systemRule, systemRuleState, systemRuleEnabled, loadEvents, loadSystemRule } = useEventCatalog();
const delistVisible = ref(false);

// 读取失败时写“读取失败”，不能当成已启用。
const systemRuleLine = computed(() => {
  if (systemRuleState.value === "loading") return "系统订阅：读取中";
  if (systemRuleState.value === "failed") return "系统订阅：读取失败";
  if (!systemRule.value) return "没有找到系统订阅：发现的下架不会被处理";
  return `系统订阅「${systemRule.value.name}」：${systemRule.value.enabled ? "已启用" : "已停用"}`;
});

onMounted(() => {
  void loadEvents();
  void loadSystemRule();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.page-head h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
}

.delist-text {
  max-width: 760px;
}

.delist-text h4 {
  margin: 16px 0 4px;
  font-size: 14px;
  font-weight: 600;
}

.delist-text p {
  margin: 0;
  color: var(--color-text-2);
  font-size: 13px;
  line-height: 1.7;
}

.delist-rule {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin: 20px 0 0;
  font-size: 13px;
}
</style>
