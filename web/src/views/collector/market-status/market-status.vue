<template>
  <div class="moox-page">
    <div class="moox-inner">
      <div class="page-head">
        <h2>市场状态</h2>
        <a-button :loading="loading" @click="load">
          <template #icon><icon-refresh /></template>
          刷新
        </a-button>
      </div>

      <section class="status-band">
        <a-table row-key="market_id" size="small" :data="rows" :loading="loading" :pagination="false" :bordered="{ cell: true }">
          <template #columns>
            <a-table-column title="市场" data-index="market_id" :width="190" />
            <a-table-column title="空间" data-index="space_id" :width="190" />
            <a-table-column title="准备状态" :width="150">
              <template #cell="{ record }">
                <a-tag :color="statusColor(record.readiness_status)">{{ record.readiness_status || 'unknown' }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="运行时" :width="110" align="center">
              <template #cell="{ record }">
                <a-tag :color="record.runtime_enabled ? 'green' : 'gray'">{{ record.runtime_enabled ? '已启用' : '未启用' }}</a-tag>
              </template>
            </a-table-column>
            <a-table-column title="Provider" data-index="runtime_provider_count" :width="110" align="right" />
            <a-table-column title="标的类型">
              <template #cell="{ record }">
                <a-space wrap><a-tag v-for="item in record.instrument_types" :key="item">{{ item }}</a-tag></a-space>
              </template>
            </a-table-column>
          </template>
        </a-table>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import { getMarketStatus, listMarketModules, type MarketModule } from '@/api/collector-market';

type StatusRow = MarketModule & { runtime_provider_count: number };
const loading = ref(false);
const rows = ref<StatusRow[]>([]);

const statusColor = (status: string) => (status === 'ready' ? 'green' : status === 'not_ready' ? 'orange' : 'gray');

const load = async () => {
  loading.value = true;
  try {
    const modules = await listMarketModules();
    rows.value = await Promise.all(
      modules.map(async (module) => {
        const detail = await getMarketStatus(module.market_id);
        return { ...module, ...detail.module, runtime_provider_count: detail.runtime_provider_count ?? 0 };
      })
    );
  } catch (error) {
    Message.error(error instanceof Error ? error.message : '加载市场状态失败');
  } finally {
    loading.value = false;
  }
};

onMounted(load);
</script>

<style scoped>
.page-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-head h2 { margin: 0; font-size: 20px; letter-spacing: 0; }
.status-band { width: 100%; }
</style>
