<template>
  <div class="moox-page logical-page" :class="{ 'is-embedded': embedded }">
    <div class="moox-inner">
      <div class="page-head">
        <a-space>
          <a-button :loading="loading" aria-label="刷新组合账户" @click="load">
            <template #icon><icon-refresh /></template>
            刷新
          </a-button>
          <a-button type="primary" status="success" @click="createVisible = true">
            <template #icon><icon-plus /></template>
            新建组合账户
          </a-button>
        </a-space>
      </div>

      <div class="summary-grid">
        <div class="summary-card">
          <span>组合账户</span>
          <strong>{{ rows.length }}</strong>
          <small>当前空间总数</small>
        </div>
        <div class="summary-card summary-card--green">
          <span>运行中</span>
          <strong>{{ stateCounts.active }}</strong>
          <small>自动执行已开启</small>
        </div>
        <div class="summary-card summary-card--orange">
          <span>已暂停</span>
          <strong>{{ stateCounts.paused }}</strong>
          <small>等待人工恢复</small>
        </div>
        <div class="summary-card summary-card--red">
          <span>未就绪</span>
          <strong>{{ stateCounts.notReady }}</strong>
          <small>存在阻断原因</small>
        </div>
      </div>

      <div class="table-toolbar">
        <a-space wrap>
          <span class="toolbar-label">筛选</span>
          <a-select v-model="stateFilter" allow-clear placeholder="自动执行状态" style="width: 150px">
            <a-option value="ACTIVE">运行中</a-option>
            <a-option value="PAUSED">已暂停</a-option>
          </a-select>
          <a-select v-model="executionFilter" allow-clear placeholder="执行模式" style="width: 130px">
            <a-option :value="1">模拟</a-option>
            <a-option :value="2">实盘</a-option>
          </a-select>
        </a-space>
        <span class="table-count">显示 {{ filteredRows.length }} 条</span>
      </div>

      <a-empty v-if="!loading && !filteredRows.length" description="暂无组合账户" />
      <a-table
        v-else
        row-key="logical_account_id"
        :data="filteredRows"
        :loading="loading"
        :pagination="{ pageSize: 20 }"
        :scroll="{ x: 1040 }"
      >
        <template #columns>
          <a-table-column title="组合账户" :width="220">
            <template #cell="{ record }">
              <a-link @click="openDetail(record)">{{ record.name }}</a-link>
              <div class="muted">{{ record.logical_account_id }}</div>
            </template>
          </a-table-column>
          <a-table-column title="账户类型" :width="100">
            <template #cell="{ record }">{{ localMarketTypeLabels[record.market_type] }}</template>
          </a-table-column>
          <a-table-column title="执行模式" :width="100">
            <template #cell="{ record }">{{ localExecutionModeLabels[record.execution_mode] }}</template>
          </a-table-column>
          <a-table-column title="控制模式" :width="100">
            <template #cell="{ record }">{{ controlModeLabel(record.control_mode) }}</template>
          </a-table-column>
          <a-table-column title="结算资产" data-index="settlement_asset" :width="110" />
          <a-table-column title="自动执行" :width="110">
            <template #cell="{ record }">
              <a-tag :color="record.automation_state === 'ACTIVE' ? 'green' : 'orange'">
                {{ record.control_mode === 2 ? "不适用" : automationStateLabel(record.automation_state) }}
              </a-tag>
            </template>
          </a-table-column>
          <a-table-column title="就绪状态" :width="110">
            <template #cell="{ record }">
              <a-tag :color="record.ready ? 'green' : 'red'">{{ record.ready ? "就绪" : "未就绪" }}</a-tag>
            </template>
          </a-table-column>
          <a-table-column title="策略实例" :width="180" :ellipsis="true" :tooltip="true">
            <template #cell="{ record }">{{ record.owner_instance_id || "-" }}</template>
          </a-table-column>
          <a-table-column title="成员数" :width="80">
            <template #cell="{ record }">{{ record.members?.length || 0 }}</template>
          </a-table-column>
          <a-table-column title="操作" :width="90" fixed="right">
            <template #cell="{ record }">
              <a-button size="mini" type="text" @click="openDetail(record)">查看</a-button>
            </template>
          </a-table-column>
        </template>
      </a-table>

      <a-modal v-model:visible="createVisible" width="min(520px, calc(100vw - 24px))" title="新建组合账户" :on-before-ok="create">
        <a-alert v-if="dialogError" type="error" show-icon class="dialog-alert">{{ dialogError }}</a-alert>
        <a-form :model="createForm" auto-label-width>
          <a-form-item label="名称" required><a-input v-model="createForm.name" /></a-form-item>
          <a-form-item label="控制模式" required>
            <a-radio-group v-model="createForm.control_mode" type="button">
              <a-radio :value="1">策略驱动</a-radio>
              <a-radio :value="2">自主下单</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="账户类型" required>
            <a-radio-group v-model="createForm.market_type" type="button">
              <a-radio :value="1">现货</a-radio>
              <a-radio :value="2">合约</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="执行模式" required>
            <a-radio-group v-model="createForm.execution_mode" type="button">
              <a-radio :value="1">模拟</a-radio>
              <a-radio :value="2">实盘</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="结算资产" required><a-input v-model="createForm.settlement_asset" /></a-form-item>
        </a-form>
      </a-modal>

      <a-drawer
        v-model:visible="detailVisible"
        width="min(860px, 100vw)"
        title="组合账户详情"
        :footer="false"
        @cancel="closeDetail"
      >
        <template v-if="selected">
          <div class="detail-head">
            <div>
              <h3>{{ selected.name }}</h3>
              <span>{{ selected.logical_account_id }}</span>
            </div>
            <a-space wrap>
              <a-button
                v-if="selected.control_mode === 1 && selected.automation_state === 'ACTIVE'"
                status="warning"
                @click="requestPause"
                >暂停自动执行</a-button
              >
              <a-button
                v-else-if="selected.control_mode === 1"
                type="primary"
                status="success"
                :disabled="!selected.ready"
                @click="resume"
                >恢复自动执行</a-button
              >
              <a-button status="danger" @click="openFlatten">逐账户清仓</a-button>
            </a-space>
          </div>
          <a-alert v-if="selected.control_mode === 1 && selected.pause_reason" type="warning" show-icon class="section">
            {{ selected.pause_reason }}
          </a-alert>
          <a-alert v-if="selected.readiness_reasons?.length" type="error" show-icon class="section">
            {{ selected.readiness_reasons.join("；") }}
          </a-alert>
          <a-alert
            v-if="selected.control_mode === 1 && selected.automation_state === 'PAUSED' && target"
            type="info"
            show-icon
            class="section"
          >
            {{ targetExpired ? "目标已过期，恢复后等下一个目标。" : "目标已保存但不会执行，恢复后继续收敛。" }}
          </a-alert>

          <a-descriptions :column="detailColumns" bordered class="section">
            <a-descriptions-item label="控制模式">{{ controlModeLabel(selected.control_mode) }}</a-descriptions-item>
            <a-descriptions-item label="自动执行">{{
              selected.control_mode === 2 ? "不适用" : automationStateLabel(selected.automation_state)
            }}</a-descriptions-item>
            <a-descriptions-item label="就绪状态">{{ selected.ready ? "就绪" : "未就绪" }}</a-descriptions-item>
            <a-descriptions-item label="策略实例">{{ selected.owner_instance_id || "-" }}</a-descriptions-item>
            <a-descriptions-item label="运行会话">{{
              target?.session_id || selected.owner_session_id || "-"
            }}</a-descriptions-item>
            <a-descriptions-item label="目标 K 线时间">{{ formatTargetTime(target?.bar_end_time) }}</a-descriptions-item>
            <a-descriptions-item label="目标有效至">{{ formatTargetTime(target?.valid_until) }}</a-descriptions-item>
          </a-descriptions>

          <div class="section">
            <div class="section-title">
              <h3>资金曲线</h3>
              <span>结算资产：{{ selected.settlement_asset }}</span>
            </div>
            <equity-curve :logical-account-id="selected.logical_account_id" />
          </div>

          <div class="section-title">
            <h3>执行账户</h3>
            <a-button
              size="small"
              type="primary"
              status="success"
              :disabled="selected.automation_state !== 'PAUSED'"
              @click="openMember"
              ><template #icon><icon-plus /></template>添加成员</a-button
            >
          </div>
          <a-alert v-if="longNotReady.length" type="error" show-icon class="section">
            成员 {{ longNotReady.join("、") }} 未就绪，整个组合账户暂停收敛。
          </a-alert>
          <div class="member-hint">优先级数字越小越先使用，当前账户资金用完才用下一个账户。</div>
          <a-table size="small" row-key="trading_account_id" :data="selected.members" :pagination="false" :scroll="{ x: 1180 }">
            <template #columns>
              <a-table-column title="执行账户" :width="170">
                <template #cell="{ record }">
                  {{ memberAccount(record)?.name || record.trading_account_id }}
                  <div class="muted">{{ exchangeLabels[memberAccount(record)?.exchange ?? 0] }}</div>
                </template>
              </a-table-column>
              <a-table-column title="启用" :width="70">
                <template #cell="{ record }">{{ record.enabled ? "是" : "否" }}</template>
              </a-table-column>
              <a-table-column title="优先级" data-index="priority" :width="80" />
              <a-table-column title="就绪" :width="80">
                <template #cell="{ record }">
                  <a-tag :color="memberAccount(record)?.ready ? 'green' : 'red'">{{
                    memberAccount(record)?.ready ? "就绪" : "未就绪"
                  }}</a-tag>
                </template>
              </a-table-column>
              <a-table-column title="权益" :width="110">
                <template #cell="{ record }">{{ memberAccount(record)?.snapshot?.equity || "-" }}</template>
              </a-table-column>
              <a-table-column title="可用资金" :width="110">
                <template #cell="{ record }">{{ memberAccount(record)?.snapshot?.available_funds || "-" }}</template>
              </a-table-column>
              <a-table-column title="持仓数" :width="80">
                <template #cell="{ record }">{{ memberPositionCount(record.trading_account_id) }}</template>
              </a-table-column>
              <a-table-column title="最近错误" :width="170" :ellipsis="true" :tooltip="true">
                <template #cell="{ record }">{{ memberAccount(record)?.last_error || "-" }}</template>
              </a-table-column>
              <a-table-column title="操作" :width="310" fixed="right">
                <template #cell="{ record }">
                  <a-space>
                    <a-button
                      size="mini"
                      type="text"
                      :disabled="!record.enabled || ![1, 2].includes(selected.control_mode)"
                      @click="openManual(record.trading_account_id)"
                      >{{ orderEntryLabel }}</a-button
                    >
                    <a-button size="mini" type="text" @click="openOrders(record.trading_account_id)">查看订单</a-button>
                    <a-tooltip
                      :content="selected.automation_state === 'PAUSED' ? '修改优先级和启用状态' : '暂停组合账户后才能编辑'"
                    >
                      <a-button
                        size="mini"
                        type="text"
                        :disabled="selected.automation_state !== 'PAUSED'"
                        @click="editMember(record)"
                        >编辑</a-button
                      >
                    </a-tooltip>
                    <a-popconfirm content="确认移除此成员？" @ok="removeMember(record.trading_account_id)">
                      <a-button size="mini" type="text" status="danger" :disabled="selected?.automation_state !== 'PAUSED'"
                        >移除</a-button
                      >
                    </a-popconfirm>
                  </a-space>
                </template>
              </a-table-column>
            </template>
          </a-table>

          <div class="section-title">
            <h3>目标与实际持仓</h3>
            <span>{{ targetStatusLabel(target?.status) }}</span>
          </div>
          <a-alert v-if="targetExpired && hasDifference" type="warning" show-icon class="section">
            目标已过期，持仓停在当前状态，等待下一个目标。
          </a-alert>
          <a-table size="small" row-key="instrument_id" :data="comparison" :pagination="false" :scroll="{ x: 600 }">
            <template #columns>
              <a-table-column title="交易标的" data-index="instrument_id" />
              <a-table-column title="目标数量" data-index="target" :width="110" />
              <a-table-column title="实际数量" data-index="actual" :width="110" />
              <a-table-column title="差额" data-index="difference" :width="110" />
              <a-table-column title="状态" data-index="status" />
              <a-table-column title="各成员持仓" data-index="members" />
            </template>
          </a-table>
          <a-empty v-if="target && !target.targets.length" description="空目标：全部归零" />
          <a-alert v-if="target?.last_error" type="warning" show-icon class="section">{{ target.last_error }}</a-alert>

          <template v-if="action">
            <div class="section-title">
              <h3>最近人工操作</h3>
              <span>{{ action.action_id }}</span>
            </div>
            <a-descriptions :column="Math.min(detailColumns, 2)" bordered>
              <a-descriptions-item label="类型">{{ actionTypeLabel(action.action_type) }}</a-descriptions-item>
              <a-descriptions-item label="状态">{{ actionStateLabel(action.status) }}</a-descriptions-item>
              <a-descriptions-item label="原因">{{ action.reason }}</a-descriptions-item>
              <a-descriptions-item label="错误">{{ action.last_error || "-" }}</a-descriptions-item>
              <a-descriptions-item v-if="submittedOrder" label="订单编号">{{ submittedOrder.order_id }}</a-descriptions-item>
              <a-descriptions-item v-if="submittedOrder" label="订单状态">{{
                orderStateLabels[submittedOrder.state] || submittedOrder.state
              }}</a-descriptions-item>
            </a-descriptions>
            <a-table
              v-if="flattenResult"
              size="small"
              row-key="trading_account_id"
              :data="flattenResult.accounts"
              :pagination="false"
              class="section"
            >
              <template #columns>
                <a-table-column title="执行账户" data-index="trading_account_id" />
                <a-table-column title="状态" :width="100">
                  <template #cell="{ record }">{{ flattenAccountStateLabel(record.status) }}</template>
                </a-table-column>
                <a-table-column title="剩余仓位">
                  <template #cell="{ record }">
                    <div v-for="item in record.remaining_positions || []" :key="`${item.instrument_id}-${item.asset}`">
                      {{ item.instrument_id || item.asset }}: {{ item.quantity }} ({{ item.reason }})
                    </div>
                    <span v-if="!record.remaining_positions?.length">无</span>
                  </template>
                </a-table-column>
                <a-table-column title="错误" data-index="error" />
              </template>
            </a-table>
          </template>
        </template>
      </a-drawer>

      <a-modal
        v-model:visible="reasonVisible"
        width="min(520px, calc(100vw - 24px))"
        :title="reasonMode === 'pause' ? '暂停组合账户' : '逐账户清仓'"
        :on-before-ok="submitReason"
      >
        <a-alert v-if="dialogError" type="error" show-icon class="dialog-alert">{{ dialogError }}</a-alert>
        <a-alert v-if="reasonMode === 'flatten'" type="warning" show-icon class="section">
          将分别清空每个执行账户的风险仓位，不进行跨账户净额；完成后保持暂停。
        </a-alert>
        <a-form :model="reasonForm" auto-label-width>
          <a-form-item label="原因" required><a-input v-model="reasonForm.reason" /></a-form-item>
        </a-form>
      </a-modal>

      <a-modal
        v-model:visible="memberVisible"
        width="min(520px, calc(100vw - 24px))"
        :title="memberEditing ? '编辑成员' : '添加执行账户'"
        :on-before-ok="addMember"
      >
        <a-alert v-if="dialogError" type="error" show-icon class="dialog-alert">{{ dialogError }}</a-alert>
        <a-form :model="memberForm" auto-label-width>
          <a-alert v-if="!memberEditing" type="info" class="dialog-alert">每个成员必须是不同的交易所账户。</a-alert>
          <a-form-item label="执行账户" required>
            <a-select v-model="memberForm.trading_account_id" allow-search :disabled="memberEditing">
              <a-option v-for="item in eligibleAccounts" :key="item.trading_account_id" :value="item.trading_account_id">
                {{ item.name }} ({{ item.trading_account_id }})
              </a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="优先级" extra="数字越小越先使用"
            ><a-input-number v-model="memberForm.priority" :min="0"
          /></a-form-item>
          <a-form-item label="启用"><a-switch v-model="memberForm.enabled" /></a-form-item>
          <a-form-item v-if="!memberEditing" label="接管已有敞口" extra="只有进入目标的标的才会调整，其他资产不动">
            <a-checkbox v-model="memberForm.adopt_existing_exposure" />
          </a-form-item>
        </a-form>
      </a-modal>

      <a-modal
        v-model:visible="manualVisible"
        width="min(520px, calc(100vw - 24px))"
        :title="orderEntryLabel"
        :ok-text="pendingSubmission ? '使用原请求重试' : orderEntryLabel"
        :on-before-ok="submitManual"
        :ok-loading="submitting"
      >
        <a-alert v-if="capabilities && !capabilities.can_place_order" type="error" show-icon class="section">
          {{ capabilities.unavailable_reason || "当前账户不可下单" }}
        </a-alert>
        <a-alert v-if="selected?.control_mode === 1" type="warning" show-icon class="section"
          >提交前会暂停整个组合账户并取消活动目标订单。</a-alert
        >
        <a-alert v-if="submissionError" type="warning" show-icon class="section">{{ submissionError }}</a-alert>
        <a-form :model="manualForm" auto-label-width :disabled="!!pendingSubmission">
          <a-form-item label="交易标的" required :extra="instrumentRuleHint(selectedInstrument)">
            <a-select v-model="manualForm.instrument_id" allow-search placeholder="搜索交易标的">
              <a-option v-for="item in instruments" :key="item.instrument_id" :value="item.instrument_id">{{
                item.instrument_id
              }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="方向" required>
            <a-radio-group v-model="manualForm.side" type="button">
              <a-radio :value="1">买入</a-radio><a-radio :value="2">卖出</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="订单类型">
            <a-radio-group v-model="manualForm.order_type" type="button">
              <a-radio v-if="capabilities?.order_types?.includes(1)" :value="1">市价</a-radio>
              <a-radio v-if="capabilities?.order_types?.includes(2)" :value="2">限价</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item v-if="manualForm.order_type === 2" label="成交策略">
            <a-select v-model="manualForm.fill_policy">
              <a-option v-for="policy in capabilities?.fill_policies || [1, 2, 3]" :key="policy" :value="policy">{{
                ["", "一直有效（GTC）", "立即成交（IOC）", "全部成交（FOK）"][policy]
              }}</a-option>
            </a-select>
          </a-form-item>
          <a-form-item label="数量" required><a-input v-model="manualForm.quantity" /></a-form-item>
          <a-form-item v-if="manualForm.order_type === 2" label="限价" required
            ><a-input v-model="manualForm.limit_price"
          /></a-form-item>
          <a-form-item label="原因" required><a-input v-model="manualForm.reason" /></a-form-item>
          <a-form-item label="提交截止时间"><a-input v-model="manualForm.deadline_at" type="datetime-local" /></a-form-item>
        </a-form>
      </a-modal>
    </div>
  </div>
</template>

<script setup lang="ts">
import { createSubmitScope } from "@/utils/submit-scope";
import { computed, onUnmounted, reactive, ref, watch } from "vue";
import { useWindowSize } from "@vueuse/core";
import { Message, Modal } from "@arco-design/web-vue";
import { useRoute, useRouter } from "vue-router";
import { useSpaceStore } from "@/store/modules/space";
import { selectedSpaceGeneration } from "@/api/admin/space-header";
import { createClientId } from "@/utils/client-id";
import {
  addLogicalAccountMember,
  createLogicalAccount,
  getExecutionCapabilities,
  flattenLogicalAccount,
  getLogicalAccount,
  getLogicalAccountTarget,
  getOperatorAction,
  getOrder,
  listHoldings,
  listPositions,
  listTradableInstruments,
  listTradingAccounts,
  listLogicalAccounts,
  parseFlattenResult,
  pauseLogicalAccount,
  placeManualOrder,
  submitOrder,
  TradeResponseError,
  removeLogicalAccountMember,
  resumeLogicalAccount,
  logicalAutomationStateLabels,
  targetStateLabels,
  actionStateLabels,
  orderStateLabels,
  exchangeLabels
} from "@/api/trade";
import type {
  TradingAccount,
  LogicalAccount,
  LogicalAccountTarget,
  OperatorAction,
  ExecutionCapabilities,
  SubmitOrderReq,
  Order,
  TradableInstrument
} from "@/api/trade";
import { instrumentRuleHint, orderRuleError } from "./order-rules";
import {
  actualFromHoldings,
  actualFromPositions,
  buildComparison,
  memberPositionsOf,
  withMemberDetail,
  type MemberPositions
} from "./target-comparison";
import EquityCurve from "./equity-curve.vue";
import { createLatestRequestGuard } from "@/utils/latest-request";

defineOptions({ name: "trading-logical-accounts" });
withDefaults(defineProps<{ embedded?: boolean }>(), { embedded: false });
const rows = ref<LogicalAccount[]>([]);
const { width: viewportWidth } = useWindowSize();
const detailColumns = computed(() => (viewportWidth.value < 600 ? 1 : 3));
const accounts = ref<TradingAccount[]>([]);
const selected = ref<LogicalAccount | null>(null);
const target = ref<LogicalAccountTarget | null>(null);
const action = ref<OperatorAction | null>(null);
const submittedOrder = ref<Order | null>(null);
const pendingSubmission = ref<{ mode: 1 | 2; request: SubmitOrderReq } | null>(null);
const submitting = ref(false);
const submissionError = ref("");
const dialogError = ref("");
const memberEditing = ref(false);
const memberPositions = ref<Record<string, number>>({});
const memberBreakdown = ref<MemberPositions>({});
const notReadySince = ref<Record<string, number>>({});
const capabilities = ref<ExecutionCapabilities | null>(null);
const instruments = ref<TradableInstrument[]>([]);
const actualPositions = ref<Record<string, number>>({});
let detailPoller: ReturnType<typeof setInterval> | null = null;
const loading = ref(false);
const createVisible = ref(false);
const detailVisible = ref(false);
const reasonVisible = ref(false);
const reasonMode = ref<"pause" | "flatten">("pause");
const memberVisible = ref(false);
const manualVisible = ref(false);
const actionPoller = ref<ReturnType<typeof setInterval> | null>(null);
const route = useRoute();
const router = useRouter();
const stateFilter = ref("");
const executionFilter = ref<number | undefined>(undefined);
const routeRequests = createLatestRequestGuard();
const detailRequests = createLatestRequestGuard();
const actionPollRequests = createLatestRequestGuard();
const capabilityRequests = createLatestRequestGuard();
const createForm = reactive({
  name: "",
  control_mode: 1 as 1 | 2,
  execution_mode: 1 as 1 | 2,
  market_type: 1 as 1 | 2,
  settlement_asset: "USDT"
});
const reasonForm = reactive({ action_id: "", reason: "" });
const memberForm = reactive({ trading_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
const manualForm = reactive({
  action_id: "",
  trading_account_id: "",
  client_order_id: "",
  instrument_id: "",
  order_type: 1 as 1 | 2,
  fill_policy: 0 as 0 | 1 | 2 | 3,
  side: 1 as 1 | 2,
  position_side: 1 as const,
  quantity: "",
  limit_price: "",
  reason: "",
  deadline_at: ""
});
const selectedInstrument = computed(() => instruments.value.find(item => item.instrument_id === manualForm.instrument_id));
watch(
  () => manualForm.order_type,
  type => {
    if (type === 2 && !manualForm.fill_policy) manualForm.fill_policy = 1;
  }
);
const localMarketTypeLabels: Record<number, string> = { 0: "-", 1: "现货", 2: "合约" };
const localExecutionModeLabels: Record<number, string> = { 0: "-", 1: "模拟", 2: "实盘" };
const orderEntryLabel = computed(() => (selected.value?.control_mode === 2 ? "下单" : "接管并下单"));
function controlModeLabel(mode: number) {
  return ({ 1: "策略驱动", 2: "自主下单" } as Record<number, string>)[mode] || "未知";
}
const flattenResult = computed(() => parseFlattenResult(action.value || undefined));
const filteredRows = computed(() =>
  rows.value.filter(item => {
    const matchesState = !stateFilter.value || (item.control_mode === 1 && item.automation_state === stateFilter.value);
    const matchesExecution = executionFilter.value === undefined || item.execution_mode === executionFilter.value;
    return matchesState && matchesExecution;
  })
);
const stateCounts = computed(() => ({
  active: rows.value.filter(item => item.automation_state === "ACTIVE").length,
  paused: rows.value.filter(item => item.control_mode === 1 && item.automation_state === "PAUSED").length,
  notReady: rows.value.filter(item => !item.ready).length
}));
const comparison = computed(() =>
  target.value
    ? withMemberDetail(
        buildComparison(target.value.targets, target.value.blocked_targets || [], actualPositions.value, {
          converged: target.value.status === "CONVERGED",
          includeUntargeted: selected.value?.market_type === 2
        }),
        memberBreakdown.value,
        id => accounts.value.find(item => item.trading_account_id === id)?.name || id
      )
    : []
);
const hasDifference = computed(
  () => target.value?.status !== "CONVERGED" && comparison.value.some(row => Number(row.difference) !== 0)
);
const targetExpired = computed(() => {
  const validUntil = Number(target.value?.valid_until);
  return target.value?.status === "EXPIRED" || (Number.isFinite(validUntil) && validUntil > 0 && validUntil <= Date.now());
});
function targetStatusLabel(status?: string) {
  return status ? targetStateLabels[status.toUpperCase()] || "未知" : "暂无";
}
function formatTargetTime(value?: number | string) {
  if (value === undefined || value === null || value === "") return "-";
  const numeric = typeof value === "number" ? value : Number(value);
  const date = Number.isFinite(numeric) ? new Date(numeric) : new Date(String(value));
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}
function actionTypeLabel(type?: string) {
  const labels: Record<string, string> = {
    FLATTEN: "逐账户清仓",
    MANUAL_ORDER: "接管并下单",
    SUBMIT_ORDER: "下单",
    CANCEL_ORDER: "撤单"
  };
  return type ? labels[type.toUpperCase()] || "未知" : "-";
}
function actionStateLabel(status?: string) {
  if (status === "COMPLETED" && ["SUBMIT_ORDER", "MANUAL_ORDER"].includes(action.value?.action_type || "")) return "提交阶段完成";
  return status ? actionStateLabels[status.toUpperCase()] || "未知" : "-";
}
function flattenAccountStateLabel(status?: string) {
  if (!status) return "-";
  return actionStateLabels[status.toUpperCase()] || "未知";
}
function automationStateLabel(status?: string) {
  return status ? logicalAutomationStateLabels[status.toUpperCase()] || "未知" : "-";
}
const eligibleAccounts = computed(() => {
  const memberIds = new Set(
    rows.value
      .flatMap(row => row.members || [])
      .filter(member => member.enabled)
      .map(member => member.trading_account_id)
  );
  const first = selected.value?.members[0];
  const environment = first ? memberAccount(first)?.live?.environment : undefined;
  return accounts.value.filter(
    item =>
      selected.value &&
      item.execution_mode === selected.value.execution_mode &&
      item.market_type === selected.value.market_type &&
      item.settlement_asset === selected.value.settlement_asset &&
      (environment === undefined || item.live?.environment === environment) &&
      !memberIds.has(item.trading_account_id) &&
      !selected.value.members.some(member => member.trading_account_id === item.trading_account_id)
  );
});
function memberAccount(member: { trading_account_id: string }) {
  return accounts.value.find(item => item.trading_account_id === member.trading_account_id);
}
function memberPositionCount(tradingAccountId: string) {
  return memberPositions.value[tradingAccountId] ?? "-";
}
// 每笔成交后账户都会短暂未就绪，超过 10 秒才提示，否则提示会闪烁。
const longNotReady = computed(() =>
  (selected.value?.members || [])
    .filter(member => member.enabled && notReadySince.value[member.trading_account_id] !== undefined)
    .filter(member => Date.now() - notReadySince.value[member.trading_account_id] >= 10_000)
    .map(member => memberAccount(member)?.name || member.trading_account_id)
);
function trackNotReady() {
  const next: Record<string, number> = {};
  for (const member of selected.value?.members || []) {
    if (member.enabled && memberAccount(member)?.ready === false) {
      next[member.trading_account_id] = notReadySince.value[member.trading_account_id] ?? Date.now();
    }
  }
  notReadySince.value = next;
}

// 空间切换后，旧空间迟到的响应不能覆盖新空间的列表：只有最新一次请求可以更新页面。
const listRequests = createLatestRequestGuard();

async function load() {
  const request = listRequests.begin();
  loading.value = true;
  try {
    // 统计卡片和筛选都要看全量，这里一次取完，表格在前端分页。
    const rsp = await listLogicalAccounts({ page: 1, size: 500 });
    if (!request.isLatest()) return;
    rows.value = rsp.logical_accounts || [];
    await openRouteDetail();
  } finally {
    if (request.isLatest()) loading.value = false;
  }
}
async function create() {
  if (!createForm.name.trim() || !createForm.settlement_asset.trim()) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  const submission = createScope.begin();
  dialogError.value = "";
  try {
    await createLogicalAccount({
      ...createForm,
      name: createForm.name.trim(),
      settlement_asset: createForm.settlement_asset.trim().toUpperCase()
    });
  } catch (error) {
    if (submission.isCurrent()) dialogError.value = requestErrorMessage(error);
    return false;
  }
  if (!submission.isCurrent()) return false;
  void load();
  return true;
}
function requestErrorMessage(error: unknown) {
  return error instanceof Error ? error.message : "请求失败，请稍后重试";
}
async function openDetail(item: LogicalAccount) {
  stopActionPolling();
  detailRequests.invalidate();
  selected.value = item;
  target.value = null;
  action.value = null;
  submittedOrder.value = null;
  capabilities.value = null;
  detailVisible.value = true;
  startDetailPolling();
  await reloadDetail();
}
function startDetailPolling() {
  stopDetailPolling();
  let polling = false;
  detailPoller = setInterval(() => {
    if (polling) return;
    polling = true;
    void reloadDetail()
      .catch(() => undefined)
      .finally(() => {
        polling = false;
      });
  }, 5000);
}
function stopDetailPolling() {
  if (detailPoller) clearInterval(detailPoller);
  detailPoller = null;
}
function openOrders(tradingAccountId: string) {
  void router.push({ path: "/trading/orders", query: { trading_account_id: tradingAccountId } });
}
async function loadActualPositions(account: LogicalAccount) {
  const counts: Record<string, number> = {};
  if (account.market_type === 2) {
    const positions = (await listPositions({ logical_account_id: account.logical_account_id })).positions || [];
    for (const item of positions) counts[item.trading_account_id] = (counts[item.trading_account_id] || 0) + 1;
    memberPositions.value = counts;
    memberBreakdown.value = memberPositionsOf(positions.map(item => ({ ...item, quantity: Number(item.signed_quantity) })));
    return actualFromPositions(positions);
  }
  const members = (account.members || []).filter(member => member.enabled);
  const responses = await Promise.all(members.map(member => listHoldings(member.trading_account_id)));
  members.forEach((member, index) => {
    counts[member.trading_account_id] = (responses[index].holdings || []).filter(item => item.instrument_id).length;
  });
  memberPositions.value = counts;
  const holdings = responses.flatMap(response => response.holdings || []);
  memberBreakdown.value = memberPositionsOf(holdings.map(item => ({ ...item, quantity: Number(item.quantity) })));
  return actualFromHoldings(holdings);
}
function closeDetail() {
  stopDetailPolling();
  capabilityRequests.invalidate();
  manualVisible.value = false;
  routeRequests.invalidate();
  detailRequests.invalidate();
  detailVisible.value = false;
  stopActionPolling();
  target.value = null;
  action.value = null;
  capabilities.value = null;
  void router.replace({ query: { ...route.query, logical_account_id: undefined } });
}
async function reloadDetail() {
  if (!selected.value) return;
  const request = detailRequests.begin();
  const id = selected.value.logical_account_id;
  const [accountRsp, targetRsp] = await Promise.all([
    getLogicalAccount(id),
    getLogicalAccountTarget(id).catch(() => ({ target: undefined }))
  ]);
  if (!request.isLatest() || selected.value?.logical_account_id !== id) return;
  selected.value = accountRsp.logical_account;
  rows.value = rows.value.map(row => (row.logical_account_id === id ? accountRsp.logical_account : row));
  target.value = targetRsp.target || null;
  const [actual, accountList] = await Promise.all([
    loadActualPositions(accountRsp.logical_account).catch(() => null),
    listTradingAccounts({ page: { page: 1, size: 200 } }).catch(() => null)
  ]);
  if (accountList && request.isLatest()) {
    accounts.value = accountList.accounts || [];
    trackNotReady();
  }
  if (actual && request.isLatest() && selected.value?.logical_account_id === id) actualPositions.value = actual;
}

function refreshActionAccount() {
  const id = selected.value?.logical_account_id;
  void reloadDetail().catch(error => {
    if (detailVisible.value && selected.value?.logical_account_id === id) {
      submissionError.value = error instanceof Error ? error.message : "账户状态刷新失败";
    }
  });
}

async function openRouteDetail() {
  const requestedId = typeof route.query.logical_account_id === "string" ? route.query.logical_account_id : "";
  const request = routeRequests.begin();
  if (!requestedId) {
    if (detailVisible.value) {
      detailRequests.invalidate();
      detailVisible.value = false;
      stopActionPolling();
      stopDetailPolling();
      selected.value = null;
      target.value = null;
      action.value = null;
      capabilities.value = null;
    }
    return;
  }
  try {
    const requested = await getLogicalAccount(requestedId);
    if (!request.isLatest() || route.query.logical_account_id !== requestedId) return;
    await openDetail(requested.logical_account);
  } catch {
    if (!request.isLatest() || route.query.logical_account_id !== requestedId) return;
    await router.replace({ query: { ...route.query, logical_account_id: undefined } });
    Message.warning("组合账户不存在或无权限");
  }
}

watch(
  () => route.query.logical_account_id,
  () => {
    void openRouteDetail();
  }
);
// 原因弹窗的打开序号：过期提交的收尾只能关闭自己打开的那一次弹窗，不能关掉用户在新空间/新账户上重新打开的弹窗
let reasonOpenToken = 0;
function requestPause() {
  reasonOpenToken += 1;
  reasonMode.value = "pause";
  reasonForm.reason = "";
  dialogError.value = "";
  reasonVisible.value = true;
}
function openFlatten() {
  reasonOpenToken += 1;
  reasonMode.value = "flatten";
  reasonForm.action_id = createClientId();
  reasonForm.reason = "";
  dialogError.value = "";
  reasonVisible.value = true;
}
function abandonedReasonSubmit(openToken: number) {
  Message.info("操作已提交到原空间的组合账户，当前空间或账户已切换；请切回原空间确认操作结果");
  if (openToken === reasonOpenToken) reasonVisible.value = false;
  return true;
}
async function submitReason() {
  if (!selected.value || !reasonForm.reason.trim() || (reasonMode.value === "flatten" && !reasonForm.action_id.trim())) {
    Message.warning("请填写原因和操作编号");
    return false;
  }
  const openToken = reasonOpenToken;
  const submitSpaceId = spaceStore.selectedSpaceId;
  const submitGeneration = selectedSpaceGeneration();
  const submitAccountId = selected.value.logical_account_id;
  const stale = () =>
    spaceStore.selectedSpaceId !== submitSpaceId ||
    selectedSpaceGeneration() !== submitGeneration ||
    selected.value?.logical_account_id !== submitAccountId;
  dialogError.value = "";
  try {
    if (reasonMode.value === "pause") {
      await pauseLogicalAccount(selected.value.logical_account_id, reasonForm.reason.trim());
      if (stale()) return abandonedReasonSubmit(openToken);
    } else {
      const rsp = await flattenLogicalAccount(
        reasonForm.action_id.trim(),
        selected.value.logical_account_id,
        reasonForm.reason.trim()
      );
      // 请求已经发出但空间或所选组合账户已变化：不覆盖新对象的操作状态，也不用新空间轮询原空间的操作
      if (stale()) return abandonedReasonSubmit(openToken);
      action.value = rsp.action;
      submittedOrder.value = null;
      startActionPolling(rsp.action.action_id);
    }
  } catch (error) {
    if (openToken === reasonOpenToken) dialogError.value = requestErrorMessage(error);
    return false;
  }
  void reloadDetail().then(load);
  // 同一空间同一账户重新打开了弹窗：操作本身仍然有效（状态与轮询照常更新），但不能关闭新打开的弹窗
  return openToken === reasonOpenToken;
}
async function resume() {
  if (!selected.value) return;
  Modal.confirm({
    title: "恢复自动执行",
    content: "恢复后会按当前完整目标继续收敛，人工清仓的仓位可能重新建立。",
    onOk: async () => {
      const rsp = await resumeLogicalAccount(selected.value!.logical_account_id);
      if (rsp.warning) Message.warning(rsp.warning);
      await reloadDetail();
      await load();
    }
  });
}
async function openMember() {
  const rsp = await listTradingAccounts({ page: { page: 1, size: 200 } });
  accounts.value = rsp.accounts || [];
  Object.assign(memberForm, { trading_account_id: "", enabled: true, priority: 0, adopt_existing_exposure: false });
  memberEditing.value = false;
  dialogError.value = "";
  memberVisible.value = true;
}
function editMember(member: { trading_account_id: string; enabled: boolean; priority: number }) {
  Object.assign(memberForm, { ...member, adopt_existing_exposure: false });
  memberEditing.value = true;
  dialogError.value = "";
  memberVisible.value = true;
}
async function addMember() {
  if (!selected.value || !memberForm.trading_account_id) {
    Message.warning("请选择执行账户");
    return false;
  }
  const submission = memberScope.begin();
  const memberAccountId = selected.value.logical_account_id;
  dialogError.value = "";
  try {
    await addLogicalAccountMember({ logical_account_id: memberAccountId, ...memberForm });
  } catch (error) {
    if (submission.isCurrent()) dialogError.value = requestErrorMessage(error);
    return false;
  }
  if (!submission.isCurrent() || selected.value?.logical_account_id !== memberAccountId) return false;
  void reloadDetail().then(load);
  return true;
}
async function removeMember(exchangeAccountId: string) {
  if (!selected.value) return;
  await removeLogicalAccountMember(selected.value.logical_account_id, exchangeAccountId);
  await reloadDetail();
  await load();
}
function openManual(exchangeAccountId: string) {
  if (!selected.value || ![1, 2].includes(selected.value.control_mode)) return;
  if (pendingSubmission.value) {
    if (
      pendingSubmission.value.request.trading_account_id !== exchangeAccountId ||
      pendingSubmission.value.request.logical_account_id !== selected.value.logical_account_id
    ) {
      Message.warning("另一个账户的请求仍待确认，请先查询原操作");
      return;
    }
    manualVisible.value = true;
    startActionPolling(pendingSubmission.value.request.action_id);
    return;
  }
  submissionError.value = "";
  stopActionPolling();
  action.value = null;
  submittedOrder.value = null;
  Object.assign(manualForm, {
    action_id: createClientId(),
    trading_account_id: exchangeAccountId,
    client_order_id: createClientId().replace(/-/g, ""),
    instrument_id: "",
    order_type: 1,
    fill_policy: 0,
    side: 1,
    position_side: 1,
    quantity: "",
    limit_price: "",
    reason: "",
    deadline_at: ""
  });
  capabilities.value = null;
  const request = capabilityRequests.begin();
  getExecutionCapabilities(exchangeAccountId)
    .then(response => {
      if (!request.isLatest()) return;
      capabilities.value = response.capabilities;
    })
    .catch(() => {
      if (!request.isLatest()) return;
      capabilities.value = null;
    });
  instruments.value = [];
  listTradableInstruments(exchangeAccountId)
    .then(response => {
      if (request.isLatest()) instruments.value = response.instruments || [];
    })
    .catch(() => {
      if (request.isLatest()) instruments.value = [];
    });
  manualVisible.value = true;
}
async function submitManual() {
  if (submitting.value || !selected.value || ![1, 2].includes(selected.value.control_mode)) return false;
  if (
    !manualForm.action_id.trim() ||
    !manualForm.client_order_id.trim() ||
    !manualForm.instrument_id.trim() ||
    !manualForm.quantity.trim() ||
    !manualForm.reason.trim() ||
    (manualForm.order_type === 2 && !manualForm.limit_price.trim())
  ) {
    Message.warning("请填写所有必填字段");
    return false;
  }
  if (!pendingSubmission.value && !capabilities.value?.can_place_order) {
    Message.warning(capabilities.value?.unavailable_reason || "账户下单能力尚未确认");
    return false;
  }
  if (!pendingSubmission.value) {
    const limitPrice = manualForm.order_type === 2 ? manualForm.limit_price : undefined;
    const ruleError = orderRuleError(manualForm.quantity, limitPrice, selectedInstrument.value);
    if (ruleError) {
      Message.warning(ruleError);
      return false;
    }
    const deadline = manualForm.deadline_at ? new Date(manualForm.deadline_at).getTime() : undefined;
    if (deadline !== undefined && (!Number.isFinite(deadline) || deadline <= Date.now())) {
      Message.warning("提交截止时间必须晚于当前时间");
      return false;
    }
    pendingSubmission.value = {
      mode: selected.value.control_mode as 1 | 2,
      request: {
        ...manualForm,
        logical_account_id: selected.value.logical_account_id,
        action_id: manualForm.action_id.trim(),
        client_order_id: manualForm.client_order_id.trim(),
        instrument_id: manualForm.instrument_id.trim().toUpperCase(),
        position_side: selected.value.market_type === 2 ? 1 : undefined,
        quantity: manualForm.quantity.trim(),
        fill_policy: manualForm.order_type === 1 ? 0 : manualForm.fill_policy,
        limit_price: manualForm.order_type === 2 ? manualForm.limit_price.trim() : undefined,
        reason: manualForm.reason.trim(),
        deadline_at: deadline === undefined ? undefined : String(deadline)
      }
    };
  }
  const pending = pendingSubmission.value;
  submitting.value = true;
  submissionError.value = "";
  try {
    const { logical_account_id: logicalId, ...manualRequest } = pending.request;
    const rsp = pending.mode === 2 ? await submitOrder(pending.request) : await placeManualOrder(manualRequest);
    if (!detailVisible.value || selected.value?.logical_account_id !== logicalId) return true;
    pendingSubmission.value = null;
    action.value = rsp.action;
    submittedOrder.value = rsp.order?.order_id ? rsp.order : null;
    manualVisible.value = false;
    startActionPolling(rsp.action.action_id);
    refreshActionAccount();
    return true;
  } catch (error) {
    if (!detailVisible.value || selected.value?.logical_account_id !== pending.request.logical_account_id) return false;
    submissionError.value = error instanceof Error ? error.message : "请求未确认";
    if (error instanceof TradeResponseError) {
      if (error.response.action?.action_id === pending.request.action_id) {
        action.value = error.response.action;
        submittedOrder.value = error.response.order?.order_id ? error.response.order : null;
        pendingSubmission.value = null;
        manualVisible.value = false;
        refreshActionAccount();
      } else if (
        !error.response.action &&
        [
          "1",
          "2",
          "3",
          "5",
          "14",
          "INVALID_PARAM",
          "NO_AUTH",
          "NO_PERMISSION",
          "NOT_FOUND",
          "SPACE_NOT_FOUND",
          "CONFLICT"
        ].includes(String(error.response.ret_info.code))
      ) {
        // Only definitive pre-admission errors allow editing. Keep the keys;
        // missing/system/transport responses may still have been accepted.
        pendingSubmission.value = null;
        stopActionPolling();
        return false;
      }
    }
    startActionPolling(pending.request.action_id);
    return false;
  } finally {
    submitting.value = false;
  }
}
function startActionPolling(actionId: string) {
  stopActionPolling();
  const request = actionPollRequests.begin();
  const logicalAccountId = selected.value?.logical_account_id;
  let polling = false;
  actionPoller.value = setInterval(async () => {
    if (polling) return;
    polling = true;
    try {
      const rsp = await getOperatorAction(actionId);
      if (!request.isLatest() || selected.value?.logical_account_id !== logicalAccountId) return;
      if (action.value?.action_id !== rsp.action.action_id || !["SUBMIT_ORDER", "MANUAL_ORDER"].includes(rsp.action.action_type))
        submittedOrder.value = null;
      const actionChanged = action.value?.action_id !== rsp.action.action_id || action.value?.status !== rsp.action.status;
      action.value = rsp.action;
      if (actionChanged) refreshActionAccount();
      if (pendingSubmission.value?.request.action_id === actionId) {
        pendingSubmission.value = null;
        manualVisible.value = false;
      }
      let orderId = submittedOrder.value?.order_id;
      if (rsp.action.result_json && ["SUBMIT_ORDER", "MANUAL_ORDER"].includes(rsp.action.action_type)) {
        const progress = JSON.parse(rsp.action.result_json) as { order_id?: string };
        orderId = progress.order_id || orderId;
      }
      if (orderId) {
        const orderRsp = await getOrder(orderId);
        if (!request.isLatest() || selected.value?.logical_account_id !== logicalAccountId) return;
        submittedOrder.value = orderRsp.order;
      }
      const orderDone =
        !orderId ||
        ["FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED"].includes(submittedOrder.value?.state || "");
      if (rsp.action.status !== "RUNNING" && orderDone) stopActionPolling();
    } catch (error) {
      if (request.isLatest()) submissionError.value = error instanceof Error ? error.message : "查询暂不可用";
    } finally {
      polling = false;
    }
  }, 1500);
}
function stopActionPolling() {
  actionPollRequests.invalidate();
  if (actionPoller.value) clearInterval(actionPoller.value);
  actionPoller.value = null;
}
onUnmounted(() => {
  stopActionPolling();
  stopDetailPolling();
});
load();
// 切换空间后重新加载，丢弃旧空间的列表与进行中的操作轮询。
const spaceStore = useSpaceStore();
watch(
  () => spaceStore.selectedSpaceId,
  (next, previous) => {
    if (!next || next === previous) return;
    stopActionPolling();
    stopDetailPolling();
    listRequests.invalidate();
    rows.value = [];
    selected.value = null;
    void load();
  }
);

// 提交作用域（重新打开才使旧提交失效，@ok 自动关闭不影响正常保存）：createVisible 打开/关闭都会使进行中的保存失效，迟到的响应不能再关闭或清空重新打开的表单
const createScope = createSubmitScope();
watch(createVisible, open => {
  if (!open) return;
  createScope.invalidate();
  dialogError.value = "";
});

// 提交作用域（重新打开才使旧提交失效，@ok 自动关闭不影响正常保存）：memberVisible 打开/关闭都会使进行中的保存失效，迟到的响应不能再关闭或清空重新打开的表单
const memberScope = createSubmitScope();
watch(memberVisible, open => {
  if (open) memberScope.invalidate();
});
</script>

<style scoped>
.page-head,
.detail-head,
.section-title {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.page-head {
  margin-bottom: var(--moox-space-4);
}
.page-head h2,
.detail-head h3,
.section-title h3 {
  margin: 0 0 4px;
}
.page-head span,
.detail-head span,
.section-title span,
.muted {
  color: var(--color-text-3);
  font-size: 12px;
}
.section,
.section-title {
  margin-top: 18px;
}
.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: var(--moox-space-4);
}
.summary-card {
  display: grid;
  gap: 4px;
  min-height: 92px;
  padding: 14px 16px;
  border: 1px solid var(--color-border-2);
  border-radius: 8px;
  background: var(--color-bg-2);
}
.summary-card span,
.summary-card small {
  color: var(--color-text-3);
  font-size: 12px;
}
.summary-card strong {
  color: var(--color-text-1);
  font-size: 24px;
  line-height: 1;
}
.summary-card--green {
  border-color: rgb(var(--green-3));
  background: rgb(var(--green-1));
}
.summary-card--orange {
  border-color: rgb(var(--orange-3));
  background: rgb(var(--orange-1));
}
.summary-card--red {
  border-color: rgb(var(--red-3));
  background: rgb(var(--red-1));
}
.table-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: var(--moox-space-3);
}
.toolbar-label,
.table-count {
  color: var(--color-text-3);
  font-size: 12px;
}
.table-count {
  white-space: nowrap;
}
.logical-page :deep(.arco-table-container) {
  border-radius: 8px;
}
.logical-page :deep(.arco-table-th) {
  white-space: nowrap;
}
.dialog-alert {
  margin-bottom: 12px;
}
.member-hint {
  margin-top: 8px;
  color: var(--color-text-3);
  font-size: 12px;
}
@media (max-width: 760px) {
  .detail-head {
    flex-wrap: wrap;
  }
  .detail-head > div {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  :deep(.arco-descriptions-item-value) {
    overflow-wrap: anywhere;
  }
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .table-toolbar {
    align-items: flex-start;
    flex-direction: column;
  }
  .page-head {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
