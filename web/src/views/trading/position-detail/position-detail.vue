<template>
  <div class="moox-page">
    <div class="moox-inner">
      <SpaceContextBar />

      <a-tabs v-model:active-key="activeTab" type="rounded">
        <!-- 持仓列表 -->
        <a-tab-pane key="positions" title="持仓列表">
          <div class="page-head">
            <a-space>
              <a-select
                v-model="positionFilter.account_id"
                placeholder="选择账户"
                style="width: 220px"
                allow-clear
                @change="loadPositions"
              >
                <a-option v-for="acc in accounts" :key="acc.account_id" :value="acc.account_id">
                  {{ acc.account_name }} ({{ accountTypeLabels[acc.account_type] }})
                </a-option>
              </a-select>
              <a-input
                v-model="positionFilter.symbol"
                placeholder="交易对"
                style="width: 140px"
                allow-clear
                @press-enter="loadPositions"
              />
              <a-button type="primary" size="small" @click="loadPositions">查询</a-button>
            </a-space>
            <a-button @click="loadPositions">
              <template #icon><icon-refresh /></template>
              刷新
            </a-button>
          </div>

          <a-table
            row-key="position_id"
            size="small"
            :bordered="{ cell: true }"
            :loading="positionLoading"
            :data="positions"
            :pagination="false"
            :scroll="{ x: 'max-content' }"
          >
            <template #columns>
              <a-table-column title="账户" :width="120">
                <template #cell="{ record }">{{ accountName(record.account_id) }}</template>
              </a-table-column>
              <a-table-column title="交易所" data-index="exchange" :width="90" />
              <a-table-column title="交易对" data-index="symbol" :width="120" />
              <a-table-column title="方向" :width="70">
                <template #cell="{ record }">
                  <a-tag size="small" :color="record.pos_side === 'long' ? 'red' : record.pos_side === 'short' ? 'green' : 'gray'">
                    {{ record.pos_side || '-' }}
                  </a-tag>
                </template>
              </a-table-column>
              <a-table-column title="数量" data-index="quantity" :width="120" />
              <a-table-column title="均价" data-index="avg_price" :width="120" />
              <a-table-column title="杠杆" data-index="leverage" :width="70" />
              <a-table-column title="保证金" data-index="margin" :width="120" />
              <a-table-column title="强平价" data-index="liq_price" :width="120" />
              <a-table-column title="未实现盈亏" :width="120">
                <template #cell="{ record }">
                  <span :style="{ color: pnlColor(record.unrealized_pnl) }">{{ record.unrealized_pnl || '-' }}</span>
                </template>
              </a-table-column>
              <a-table-column title="已实现盈亏" :width="120">
                <template #cell="{ record }">
                  <span :style="{ color: pnlColor(record.realized_pnl) }">{{ record.realized_pnl || '-' }}</span>
                </template>
              </a-table-column>
              <a-table-column title="更新时间" :width="170">
                <template #cell="{ record }">{{ formatTimestamp(record.updated_at) }}</template>
              </a-table-column>
            </template>
          </a-table>
        </a-tab-pane>

        <!-- 交易通道 -->
        <a-tab-pane key="channels" title="交易通道">
          <div class="page-head">
            <h3>交易通道管理</h3>
            <a-space>
              <a-button type="primary" @click="openCreateChannel">
                <template #icon><icon-plus /></template>
                新增通道
              </a-button>
              <a-button @click="loadChannels">
                <template #icon><icon-refresh /></template>
                刷新
              </a-button>
            </a-space>
          </div>

          <a-table
            row-key="channel_id"
            size="small"
            :bordered="{ cell: true }"
            :loading="channelLoading"
            :data="channels"
            :pagination="channelPagination"
            :scroll="{ x: 'max-content' }"
            @page-change="onChannelPageChange"
            @page-size-change="onChannelPageSizeChange"
          >
            <template #columns>
              <a-table-column title="通道名称" data-index="channel_name" :width="140" />
              <a-table-column title="交易所" data-index="exchange" :width="90" />
              <a-table-column title="市场" :width="70">
                <template #cell="{ record }">{{ marketTypeLabels[record.market_type] || record.market_type }}</template>
              </a-table-column>
              <a-table-column title="账户" :width="120">
                <template #cell="{ record }">{{ accountName(record.account_id) }}</template>
              </a-table-column>
              <a-table-column title="模拟" :width="60">
                <template #cell="{ record }">
                  <a-tag v-if="record.is_simulated" size="small" color="blue">是</a-tag>
                  <span v-else>-</span>
                </template>
              </a-table-column>
              <a-table-column title="状态" :width="80">
                <template #cell="{ record }">
                  <a-tag size="small" :color="channelStatusColors[record.status]">{{ channelStatusLabels[record.status] }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="限频" data-index="rate_limit" :width="70" />
              <a-table-column title="最后心跳" :width="170">
                <template #cell="{ record }">{{ formatTimestamp(record.last_heartbeat) }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="200" align="center" fixed="right">
                <template #cell="{ record }">
                  <a-space>
                    <a-button size="mini" type="text" @click="onTestChannel(record)" :loading="record._testing">测试</a-button>
                    <a-button size="mini" type="text" @click="openEditChannel(record)">编辑</a-button>
                    <a-popconfirm content="确定删除该通道？" @ok="onDeleteChannel(record)">
                      <a-button size="mini" type="text" status="danger">删除</a-button>
                    </a-popconfirm>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>

          <!-- 新增/编辑通道弹窗 -->
          <a-modal v-model:visible="channelModalVisible" width="560px" :title="editingChannel ? '编辑通道' : '新增通道'" @ok="submitChannel">
            <a-form :model="channelForm" auto-label-width>
              <a-form-item field="channel_name" label="通道名称" required>
                <a-input v-model="channelForm.channel_name" placeholder="如：币安现货主通道" />
              </a-form-item>
              <a-form-item v-if="!editingChannel" field="exchange" label="交易所" required>
                <a-select v-model="channelForm.exchange">
                  <a-option value="binance">Binance</a-option>
                  <a-option value="okx">OKX</a-option>
                </a-select>
              </a-form-item>
              <a-form-item v-if="!editingChannel" field="market_type" label="市场类型" required>
                <a-select v-model="channelForm.market_type">
                  <a-option :value="0">现货</a-option>
                  <a-option :value="1">杠杆</a-option>
                  <a-option :value="2">永续</a-option>
                  <a-option :value="3">交割</a-option>
                </a-select>
              </a-form-item>
              <a-form-item v-if="!editingChannel" field="account_id" label="绑定账户" required>
                <a-select v-model="channelForm.account_id">
                  <a-option v-for="acc in accounts" :key="acc.account_id" :value="acc.account_id">
                    {{ acc.account_name }} ({{ accountTypeLabels[acc.account_type] }})
                  </a-option>
                </a-select>
              </a-form-item>
              <a-form-item v-if="!editingChannel" field="api_key_id" label="API凭证">
                <a-input v-model="channelForm.api_key_id" placeholder="可空，后续绑定" />
              </a-form-item>
              <a-form-item field="endpoint" label="Endpoint">
                <a-input v-model="channelForm.endpoint" placeholder="自定义接入地址" />
              </a-form-item>
              <a-form-item v-if="!editingChannel" field="is_simulated" label="模拟盘">
                <a-switch v-model="channelForm.is_simulated" />
              </a-form-item>
              <a-form-item field="rate_limit" label="限频(QPS)">
                <a-input-number v-model="channelForm.rate_limit" :min="0" />
              </a-form-item>
              <a-form-item v-if="editingChannel" field="status" label="状态">
                <a-select v-model="channelForm.status">
                  <a-option :value="0">禁用</a-option>
                  <a-option :value="1">在线</a-option>
                  <a-option :value="2">离线</a-option>
                  <a-option :value="3">异常</a-option>
                </a-select>
              </a-form-item>
            </a-form>
          </a-modal>
        </a-tab-pane>
      </a-tabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue';
import { Message } from '@arco-design/web-vue';
import SpaceContextBar from '@/components/SpaceContextBar/index.vue';
import {
  listAccounts, listPositions, listChannels, createChannel, updateChannel, deleteChannel, testChannel,
  accountTypeLabels, marketTypeLabels, channelStatusLabels, channelStatusColors, formatTimestamp,
} from '@/api/trade';
import type { Account, Position, TradeChannel, MarketType, ChannelStatus } from '@/api/trade/types';
import { defaultPagination, applyPageResult } from '@/views/data/shared/metadata-utils';

defineOptions({ name: 'position-detail' });

const activeTab = ref('positions');
const accounts = ref<Account[]>([]);

async function loadAccounts() {
  const rsp = await listAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
}

function accountName(accountId: string): string {
  const acc = accounts.value.find((a) => a.account_id === accountId);
  return acc ? acc.account_name : accountId || '-';
}

function pnlColor(val?: string): string {
  if (!val) return '';
  const n = parseFloat(val);
  if (isNaN(n)) return '';
  return n > 0 ? '#f53f3f' : n < 0 ? '#00b42a' : '';
}

// ========== 持仓列表 ==========
const positions = ref<Position[]>([]);
const positionLoading = ref(false);
const positionFilter = reactive({ account_id: '', symbol: '' });

async function loadPositions() {
  if (!positionFilter.account_id) {
    positions.value = [];
    return;
  }
  positionLoading.value = true;
  try {
    const rsp = await listPositions(positionFilter.account_id, positionFilter.symbol || undefined);
    positions.value = rsp.positions || [];
  } finally {
    positionLoading.value = false;
  }
}

// ========== 交易通道 ==========
const channels = ref<(TradeChannel & { _testing?: boolean })[]>([]);
const channelLoading = ref(false);
const channelPagination = reactive(defaultPagination());
const channelModalVisible = ref(false);
const editingChannel = ref(false);
const channelForm = reactive<{
  channel_id: string;
  channel_name: string;
  exchange: string;
  market_type: MarketType;
  account_id: string;
  api_key_id: string;
  endpoint: string;
  is_simulated: boolean;
  status: ChannelStatus;
  rate_limit: number;
}>({
  channel_id: '',
  channel_name: '',
  exchange: 'binance',
  market_type: 0,
  account_id: '',
  api_key_id: '',
  endpoint: '',
  is_simulated: false,
  status: 0,
  rate_limit: 10,
});

async function loadChannels() {
  channelLoading.value = true;
  try {
    const rsp = await listChannels({ page: { page: channelPagination.current, size: channelPagination.pageSize } });
    channels.value = rsp.channels || [];
    applyPageResult(channelPagination, rsp.page_result);
  } finally {
    channelLoading.value = false;
  }
}

function onChannelPageChange(page: number) {
  channelPagination.current = page;
  loadChannels();
}

function onChannelPageSizeChange(size: number) {
  channelPagination.current = 1;
  channelPagination.pageSize = size;
  loadChannels();
}

function resetChannelForm() {
  Object.assign(channelForm, {
    channel_id: '', channel_name: '', exchange: 'binance', market_type: 0,
    account_id: '', api_key_id: '', endpoint: '', is_simulated: false, status: 0, rate_limit: 10,
  });
}

function openCreateChannel() {
  editingChannel.value = false;
  resetChannelForm();
  channelModalVisible.value = true;
}

function openEditChannel(record: TradeChannel) {
  editingChannel.value = true;
  Object.assign(channelForm, {
    channel_id: record.channel_id,
    channel_name: record.channel_name,
    exchange: record.exchange,
    market_type: record.market_type,
    account_id: record.account_id,
    api_key_id: record.api_key_id,
    endpoint: record.endpoint,
    is_simulated: record.is_simulated,
    status: record.status,
    rate_limit: record.rate_limit,
  });
  channelModalVisible.value = true;
}

async function submitChannel() {
  if (!channelForm.channel_name) {
    Message.warning('请输入通道名称');
    return;
  }
  if (editingChannel.value) {
    await updateChannel({
      channel_id: channelForm.channel_id,
      channel_name: channelForm.channel_name,
      status: channelForm.status,
      endpoint: channelForm.endpoint || undefined,
      rate_limit: channelForm.rate_limit,
    });
    Message.success('通道已更新');
  } else {
    if (!channelForm.account_id) {
      Message.warning('请选择绑定账户');
      return;
    }
    await createChannel({
      channel_name: channelForm.channel_name,
      exchange: channelForm.exchange,
      market_type: channelForm.market_type,
      account_id: channelForm.account_id,
      api_key_id: channelForm.api_key_id || undefined,
      endpoint: channelForm.endpoint || undefined,
      is_simulated: channelForm.is_simulated,
      rate_limit: channelForm.rate_limit,
    });
    Message.success('通道已创建');
  }
  channelModalVisible.value = false;
  await loadChannels();
}

async function onDeleteChannel(record: TradeChannel) {
  await deleteChannel(record.channel_id);
  Message.success('通道已删除');
  await loadChannels();
}

async function onTestChannel(record: TradeChannel & { _testing?: boolean }) {
  record._testing = true;
  try {
    const rsp = await testChannel(record.channel_id);
    if (rsp.reachable) {
      Message.success(`连通正常，延迟 ${rsp.latency_ms}ms`);
    } else {
      Message.warning('通道不可达');
    }
  } finally {
    record._testing = false;
  }
}

onMounted(async () => {
  await loadAccounts();
  loadChannels();
});
</script>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
}

.page-head h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
</style>
