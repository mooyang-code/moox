<template>
  <div class="moox-page">
    <!-- 如果有选中项目，显示量化交易首页 -->
    <div v-if="selectedProjectId" class="trading-dashboard">
      <!-- 核心指标卡片 -->
      <div class="metrics-grid">
        <a-card class="metric-card" hoverable>
          <div class="metric-content">
            <div class="metric-icon">
              <icon-dashboard :size="32" style="color: #1677ff;" />
            </div>
            <div class="metric-info">
              <p class="metric-label">总资产</p>
              <p class="metric-value">¥{{ formatNumber(totalAssets) }}</p>
              <p class="metric-change positive">
                <icon-arrow-up :size="16" />
                +{{ assetChange.rate }}% (+¥{{ formatNumber(assetChange.amount) }})
              </p>
            </div>
          </div>
        </a-card>

        <a-card class="metric-card" hoverable>
          <div class="metric-content">
            <div class="metric-icon">
              <icon-heart-fill :size="32" style="color: #52c41a;" />
            </div>
            <div class="metric-info">
              <p class="metric-label">运行策略</p>
              <p class="metric-value">{{ runningStrategies }}</p>
              <p class="metric-detail">{{ pausedStrategies }}个暂停</p>
            </div>
          </div>
        </a-card>

        <a-card class="metric-card" hoverable>
          <div class="metric-content">
            <div class="metric-icon">
              <icon-bar-chart :size="32" style="color: #722ed1;" />
            </div>
            <div class="metric-info">
              <p class="metric-label">今日交易</p>
              <p class="metric-value">{{ todayTrades }}</p>
              <p class="metric-detail positive">盈利: {{ profitableTrades }}笔</p>
            </div>
          </div>
        </a-card>

        <a-card class="metric-card" hoverable>
          <div class="metric-content">
            <div class="metric-icon">
              <icon-exclamation-circle-fill :size="32" style="color: #fa8c16;" />
            </div>
            <div class="metric-info">
              <p class="metric-label">风险等级</p>
              <p class="metric-value risk-medium">中等</p>
              <p class="metric-detail">最大回撤: {{ maxDrawdown }}%</p>
            </div>
          </div>
        </a-card>
      </div>

      <!-- 收益曲线和策略表现 -->
      <div class="main-content">
        <!-- 收益曲线 -->
        <div class="chart-section">
          <a-card class="revenue-chart" title="收益曲线">
            <template #extra>
              <a-button-group>
                <a-button 
                  :type="timeRange === '30d' ? 'primary' : 'outline'" 
                  size="small"
                  @click="changeTimeRange('30d')"
                >
                  30天
                </a-button>
                <a-button 
                  :type="timeRange === '3m' ? 'primary' : 'outline'" 
                  size="small"
                  @click="changeTimeRange('3m')"
                >
                  3个月
                </a-button>
                <a-button 
                  :type="timeRange === '1y' ? 'primary' : 'outline'" 
                  size="small"
                  @click="changeTimeRange('1y')"
                >
                  1年
                </a-button>
              </a-button-group>
            </template>
            <div ref="revenueChartRef" class="chart-container"></div>
          </a-card>
        </div>

        <!-- 策略表现 -->
        <div class="strategy-section">
          <a-card title="策略表现">
            <template #extra>
              <a-button type="text" @click="viewAllStrategies">查看全部</a-button>
            </template>
            <div class="strategy-list">
              <div 
                v-for="(strategy, index) in strategyData" 
                :key="index" 
                class="strategy-item"
              >
                <div class="strategy-info">
                  <p class="strategy-name">{{ strategy.name }}</p>
                  <div class="strategy-stats">
                    <span 
                      :class="['strategy-return', strategy.return > 0 ? 'positive' : 'negative']"
                    >
                      {{ strategy.return > 0 ? '+' : '' }}{{ strategy.return }}%
                    </span>
                    <a-tag 
                      :color="getStrategyStatusColor(strategy.status)"
                      size="small"
                    >
                      {{ getStrategyStatusText(strategy.status) }}
                    </a-tag>
                  </div>
                </div>
                <div class="strategy-actions">
                  <a-button 
                    type="text" 
                    size="small"
                    @click="toggleStrategy(strategy)"
                  >
                    <icon-pause v-if="strategy.status === 'running'" :size="16" />
                    <icon-play-arrow v-else :size="16" />
                  </a-button>
                </div>
              </div>
            </div>
          </a-card>
        </div>
      </div>

      <!-- 快捷操作和交易日志 -->
      <div class="bottom-content">
        <!-- 快捷操作 -->
        <div class="shortcuts-section">
          <a-card title="快捷操作">
            <div class="shortcuts-grid">
              <div class="shortcut-item" @click="createStrategy">
                <div class="shortcut-icon create">
                  <icon-plus :size="24" />
                </div>
                <span class="shortcut-label">创建策略</span>
              </div>
              <div class="shortcut-item" @click="backtest">
                <div class="shortcut-icon">
                  <icon-bar-chart :size="24" />
                </div>
                <span class="shortcut-label">回测分析</span>
              </div>
              <div class="shortcut-item" @click="viewPositions">
                <div class="shortcut-icon">
                  <icon-heart-fill :size="24" />
                </div>
                <span class="shortcut-label">查看持仓</span>
              </div>
              <div class="shortcut-item" @click="riskCheck">
                <div class="shortcut-icon">
                  <icon-exclamation-circle-fill :size="24" />
                </div>
                <span class="shortcut-label">风险检查</span>
              </div>
            </div>
          </a-card>
        </div>

        <!-- 交易日志 -->
        <div class="trades-section">
          <a-card title="最近交易">
            <template #extra>
              <a-button type="text" @click="viewAllTrades">查看全部</a-button>
            </template>
            <div class="trades-list">
              <div 
                v-for="(trade, index) in tradeLog" 
                :key="index" 
                class="trade-item"
              >
                <div class="trade-info">
                  <a-tag 
                    :color="trade.action === '买入' ? 'red' : 'green'"
                    size="small"
                  >
                    {{ trade.action }}
                  </a-tag>
                  <span class="trade-symbol">{{ trade.symbol }}</span>
                </div>
                <div class="trade-details">
                  <p class="trade-price">{{ trade.price }}</p>
                  <p class="trade-time">{{ trade.time }}</p>
                </div>
              </div>
            </div>
          </a-card>
        </div>
      </div>
    </div>

    <!-- 如果没有选中项目，显示默认首页 -->
    <div v-else class="home-page">
      <!-- 常用功能 -->
      <Shortcut />
      <!-- 第三板指标 -->
      <TargetBox />
      <!-- 财务指标 -->
      <Finance />
      <!-- 数据图 -->
      <DataBox />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed, watch, nextTick, onUnmounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { Message } from '@arco-design/web-vue';
import VChart from '@visactor/vchart';
import { useProjectStore } from '@/store/modules/project';
import { storeToRefs } from 'pinia';
import Shortcut from "@/views/home/components/shortcut.vue";
import TargetBox from "@/views/home/components/target-box.vue";
import Finance from "@/views/home/components/finance.vue";
import DataBox from "@/views/home/components/data-box.vue";

const route = useRoute();
const router = useRouter();

// 使用项目状态管理
const projectStore = useProjectStore();
const { selectedProjectId } = storeToRefs(projectStore);

// 图表实例
const revenueChartRef = ref<HTMLElement>();
let revenueChart: VChart | null = null;

// 时间范围
const timeRange = ref('30d');

// 模拟数据
const totalAssets = ref(1150000);
const assetChange = ref({ rate: 15.6, amount: 15000 });
const runningStrategies = ref(8);
const pausedStrategies = ref(3);
const todayTrades = ref(24);
const profitableTrades = ref(18);
const maxDrawdown = ref(5.2);

const strategyData = ref([
  { name: '均线策略A', return: 15.6, status: 'running' },
  { name: '网格交易B', return: 12.3, status: 'running' },
  { name: '动量策略C', return: 8.9, status: 'paused' },
  { name: '套利策略D', return: 6.7, status: 'running' },
  { name: '量化选股E', return: 4.2, status: 'stopped' },
]);

const tradeLog = ref([
  { time: '14:30:25', action: '买入', symbol: 'AAPL', price: '150.25', qty: '100' },
  { time: '14:28:12', action: '卖出', symbol: 'TSLA', price: '245.80', qty: '50' },
  { time: '14:25:33', action: '买入', symbol: '000001', price: '12.45', qty: '1000' },
  { time: '14:22:18', action: '卖出', symbol: '600036', price: '35.67', qty: '200' },
]);

// 收益数据
const revenueData = ref([
  { date: '01-01', value: 100000, benchmark: 100000 },
  { date: '01-08', value: 102000, benchmark: 101000 },
  { date: '01-15', value: 105000, benchmark: 102500 },
  { date: '01-22', value: 103000, benchmark: 103000 },
  { date: '01-29', value: 108000, benchmark: 104000 },
  { date: '02-05', value: 112000, benchmark: 105500 },
  { date: '02-12', value: 115000, benchmark: 106000 },
]);

// 格式化数字
const formatNumber = (num: number) => {
  return num.toLocaleString();
};

// 获取策略状态颜色
const getStrategyStatusColor = (status: string) => {
  switch (status) {
    case 'running':
      return 'green';
    case 'paused':
      return 'orange';
    case 'stopped':
      return 'gray';
    default:
      return 'gray';
  }
};

// 获取策略状态文本
const getStrategyStatusText = (status: string) => {
  switch (status) {
    case 'running':
      return '运行中';
    case 'paused':
      return '暂停';
    case 'stopped':
      return '停止';
    default:
      return '未知';
  }
};

// 切换时间范围
const changeTimeRange = (range: string) => {
  timeRange.value = range;
  // 这里可以根据时间范围更新数据
  updateChartData();
};

// 更新图表数据
const updateChartData = () => {
  if (revenueChart) {
    // 根据时间范围生成不同的数据
    const newData = generateDataByTimeRange(timeRange.value);
    revenueChart.updateData('revenue', newData);
  }
};

// 根据时间范围生成数据
const generateDataByTimeRange = (range: string) => {
  console.log('时间范围:', range);
  // 这里可以根据实际需求生成不同时间范围的数据
  return revenueData.value;
};

// 初始化收益图表
const initRevenueChart = () => {
  if (!revenueChartRef.value) return;

  const spec = {
    type: 'line',
    data: {
      values: revenueData.value.map(item => ({
        date: item.date,
        value: item.value,
        type: '我的收益'
      })).concat(revenueData.value.map(item => ({
        date: item.date,
        value: item.benchmark,
        type: '基准'
      })))
    },
    xField: 'date',
    yField: 'value',
    seriesField: 'type',
    color: ['#1677ff', '#6B7280']
  };

  revenueChart = new VChart(spec as any, {
    dom: revenueChartRef.value
  });
  revenueChart.renderSync();
};

// 切换策略状态
const toggleStrategy = (strategy: any) => {
  if (strategy.status === 'running') {
    strategy.status = 'paused';
    Message.success(`已暂停策略: ${strategy.name}`);
  } else {
    strategy.status = 'running';
    Message.success(`已启动策略: ${strategy.name}`);
  }
};

// 快捷操作方法
const createStrategy = () => {
  Message.info('跳转到创建策略页面');
};

const backtest = () => {
  Message.info('跳转到回测分析页面');
};

const viewPositions = () => {
  Message.info('跳转到持仓查看页面');
};

const riskCheck = () => {
  Message.info('跳转到风险检查页面');
};

const viewAllStrategies = () => {
  Message.info('跳转到策略管理页面');
};

const viewAllTrades = () => {
  Message.info('跳转到交易记录页面');
};

// 监听项目ID变化
watch(selectedProjectId, (newProjectId) => {
  if (newProjectId) {
    // 当有项目ID时，加载项目相关的数据
    loadProjectData(newProjectId.toString());
  }
});

// 加载项目数据
const loadProjectData = async (projectId: string) => {
  try {
    // 这里可以调用API加载项目相关的量化交易数据
    console.log('加载项目数据:', projectId);
    
    // 初始化图表
    await nextTick();
    if (selectedProjectId.value) {
      initRevenueChart();
    }
  } catch (error) {
    console.error('加载项目数据失败:', error);
    Message.error('加载项目数据失败');
  }
};

onMounted(async () => {
  // 确保项目数据已加载
  await projectStore.fetchProjects();
  
  if (selectedProjectId.value) {
    await loadProjectData(selectedProjectId.value.toString());
  }
});

// 组件销毁时清理图表
onUnmounted(() => {
  if (revenueChart) {
    revenueChart.release();
    revenueChart = null;
  }
});
</script>

<style lang="scss" scoped>
.home-page {
  padding: $padding;
  background: $color-bg-1;
}

.trading-dashboard {
  padding: 24px;
  background: $color-bg-1;
  min-height: calc(100vh - 120px);
}

/* 指标卡片网格 */
.metrics-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 24px;
  margin-bottom: 24px;
}

.metric-card {
  .metric-content {
    display: flex;
    align-items: center;
    gap: 16px;
  }

  .metric-icon {
    flex-shrink: 0;
    width: 48px;
    height: 48px;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 8px;
    background: rgba(22, 119, 255, 0.1);
  }

  .metric-info {
    flex: 1;
    min-width: 0;
  }

  .metric-label {
    font-size: 14px;
    color: $color-text-2;
    margin: 0 0 4px 0;
  }

  .metric-value {
    font-size: 24px;
    font-weight: 600;
    color: $color-text-1;
    margin: 0 0 4px 0;
    
    &.risk-medium {
      color: #fa8c16;
    }
  }

  .metric-change, .metric-detail {
    font-size: 12px;
    margin: 0;
    display: flex;
    align-items: center;
    gap: 4px;
    
    &.positive {
      color: #52c41a;
    }
    
    &.negative {
      color: #ff4d4f;
    }
  }
}

/* 主要内容区域 */
.main-content {
  display: grid;
  grid-template-columns: 2fr 1fr;
  gap: 24px;
  margin-bottom: 24px;
}

.chart-section, .strategy-section {
  .chart-container {
    height: 300px;
    width: 100%;
  }
}

/* 策略列表 */
.strategy-list {
  .strategy-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0;
    border-bottom: 1px solid $color-border-2;
    
    &:last-child {
      border-bottom: none;
    }
  }

  .strategy-info {
    flex: 1;
    min-width: 0;
  }

  .strategy-name {
    font-size: 14px;
    font-weight: 500;
    color: $color-text-1;
    margin: 0 0 8px 0;
  }

  .strategy-stats {
    display: flex;
    align-items: center;
    gap: 8px;
  }

  .strategy-return {
    font-size: 13px;
    font-weight: 500;
    
    &.positive {
      color: #52c41a;
    }
    
    &.negative {
      color: #ff4d4f;
    }
  }

  .strategy-actions {
    flex-shrink: 0;
  }
}

/* 底部内容区域 */
.bottom-content {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 24px;
}

/* 快捷操作 */
.shortcuts-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 16px;
}

.shortcut-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 24px 16px;
  border: 2px dashed $color-border-3;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.3s ease;
  
  &:hover {
    border-color: #1677ff;
    background: rgba(22, 119, 255, 0.05);
  }
}

.shortcut-icon {
  width: 48px;
  height: 48px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 8px;
  background: $color-bg-3;
  margin-bottom: 12px;
  color: $color-text-2;
  
  &.create {
    border: 2px dashed #1677ff;
    background: rgba(22, 119, 255, 0.1);
    color: #1677ff;
  }
}

.shortcut-label {
  font-size: 14px;
  font-weight: 500;
  color: $color-text-1;
}

/* 交易列表 */
.trades-list {
  .trade-item {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 0;
    border-bottom: 1px solid $color-border-2;
    
    &:last-child {
      border-bottom: none;
    }
  }

  .trade-info {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .trade-symbol {
    font-size: 14px;
    font-weight: 500;
    color: $color-text-1;
  }

  .trade-details {
    text-align: right;
  }

  .trade-price {
    font-size: 14px;
    color: $color-text-1;
    margin: 0 0 4px 0;
  }

  .trade-time {
    font-size: 12px;
    color: $color-text-3;
    margin: 0;
  }
}

/* 响应式设计 */
@media (max-width: 1200px) {
  .main-content {
    grid-template-columns: 1fr;
  }
  
  .bottom-content {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 768px) {
  .trading-dashboard {
    padding: 16px;
  }
  
  .metrics-grid {
    grid-template-columns: 1fr;
    gap: 16px;
  }
  
  .shortcuts-grid {
    grid-template-columns: 1fr;
  }
}
</style>
