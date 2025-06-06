<template>
  <div class="candlestick-wrapper">
    <div class="chart-controls">
      <a-button-group size="small">
        <a-button 
          :type="timeframe === '1D' ? 'primary' : 'outline'" 
          @click="changeTimeframe('1D')"
        >
          日线
        </a-button>
        <a-button 
          :type="timeframe === '1H' ? 'primary' : 'outline'" 
          @click="changeTimeframe('1H')"
        >
          小时线
        </a-button>
        <a-button 
          :type="timeframe === '15M' ? 'primary' : 'outline'" 
          @click="changeTimeframe('15M')"
        >
          15分钟
        </a-button>
      </a-button-group>
      
      <div class="price-info">
        <span class="current-price" :class="priceChange >= 0 ? 'positive' : 'negative'">
          ¥{{ currentPrice.toFixed(2) }}
        </span>
        <span class="price-change" :class="priceChange >= 0 ? 'positive' : 'negative'">
          {{ priceChange >= 0 ? '+' : '' }}{{ priceChange.toFixed(2) }} 
          ({{ priceChangePercent >= 0 ? '+' : '' }}{{ priceChangePercent.toFixed(2) }}%)
        </span>
      </div>
    </div>
    
    <div ref="chartContainer" class="candlestick-chart" v-show="chartReady">
      <div v-if="!chartReady" class="chart-loading">
        图表加载中...
        <div style="font-size: 10px; margin-top: 5px;">
          容器大小将在下方显示
        </div>
      </div>
    </div>
    
    <!-- 调试信息 -->
    <div v-if="!chartReady" style="font-size: 12px; color: #999; margin-top: 10px;">
      调试信息: 等待图表初始化...
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed, nextTick } from 'vue';
import { createChart, ColorType, CandlestickSeries, HistogramSeries } from 'lightweight-charts';

const chartContainer = ref<HTMLElement>();
let chart: any = null;
let candlestickSeries: any = null;
let volumeSeries: any = null;

const timeframe = ref('1D');
const currentPrice = ref(150);
const priceChange = ref(0);
const chartReady = ref(false);

const priceChangePercent = computed(() => {
  const previousPrice = currentPrice.value - priceChange.value;
  return previousPrice !== 0 ? (priceChange.value / previousPrice) * 100 : 0;
});

// 生成简单的测试K线数据
const generateTestData = () => {
  console.log('开始生成测试数据...');
  
  const candlestickData = [];
  const volumeData = [];
  
  let basePrice = 150;
  const startDate = new Date();
  startDate.setDate(startDate.getDate() - 30); // 30天前开始
  
  for (let i = 0; i < 30; i++) {
    const date = new Date(startDate);
    date.setDate(date.getDate() + i);
    const timeString = date.toISOString().split('T')[0];
    
    // 简单的价格变化
    const change = (Math.random() - 0.5) * 4;
    const open = basePrice;
    const close = basePrice + change;
    const high = Math.max(open, close) + Math.random() * 2;
    const low = Math.min(open, close) - Math.random() * 2;
    
    candlestickData.push({
      time: timeString,
      open: parseFloat(open.toFixed(2)),
      high: parseFloat(high.toFixed(2)),
      low: parseFloat(low.toFixed(2)),
      close: parseFloat(close.toFixed(2))
    });
    
    // 根据K线涨跌设置成交量颜色
    const isUp = close >= open;
    volumeData.push({
      time: timeString,
      value: Math.floor(Math.random() * 1000000) + 500000,
      color: isUp ? 'rgba(38, 166, 154, 0.6)' : 'rgba(239, 83, 80, 0.6)' // 绿色上涨，红色下跌
    });
    
    basePrice = close;
  }
  
  // 更新当前价格
  const lastCandle = candlestickData[candlestickData.length - 1];
  const prevCandle = candlestickData[candlestickData.length - 2];
  currentPrice.value = lastCandle.close;
  priceChange.value = lastCandle.close - prevCandle.close;
  
  console.log('生成的K线数据:', candlestickData.slice(0, 3));
  console.log('生成的成交量数据:', volumeData.slice(0, 3));
  
  return { candlestickData, volumeData };
};

const changeTimeframe = (newTimeframe: string) => {
  timeframe.value = newTimeframe;
  console.log('切换时间周期:', newTimeframe);
  updateChartData();
};

const updateChartData = () => {
  console.log('开始更新图表数据...');
  
  if (!candlestickSeries || !volumeSeries) {
    console.error('图表序列未初始化');
    return;
  }
  
  try {
    const { candlestickData, volumeData } = generateTestData();
    
    console.log('设置K线数据，数量:', candlestickData.length);
    candlestickSeries.setData(candlestickData);
    
    console.log('设置成交量数据，数量:', volumeData.length);
    volumeSeries.setData(volumeData);
    
    if (chart) {
      console.log('调整图表时间轴');
      chart.timeScale().fitContent();
    }
    
    console.log('图表数据更新成功');
  } catch (error) {
    console.error('更新图表数据失败:', error);
  }
};

const initChart = async () => {
  console.log('开始初始化图表...');
  
  if (!chartContainer.value) {
    console.error('图表容器未找到');
    return;
  }
  
  try {
    console.log('使用 lightweight-charts 库');
    console.log('createChart:', typeof createChart);
    
    // 强制设置容器尺寸
    const containerElement = chartContainer.value;
    
    // 等待一个渲染周期，确保容器有尺寸
    await new Promise(resolve => setTimeout(resolve, 50));
    
    let containerWidth = containerElement.clientWidth;
    const containerHeight = 350;
    
    // 如果还是没有宽度，使用父元素的宽度或默认值
    if (containerWidth === 0) {
      const parentElement = containerElement.parentElement;
      if (parentElement) {
        containerWidth = parentElement.clientWidth || 500;
      } else {
        containerWidth = 500; // 默认宽度
      }
      console.log('使用默认宽度:', containerWidth);
    }
    
    console.log('容器尺寸:', containerWidth, 'x', containerHeight);
    
    // 创建图表
    chart = createChart(containerElement, {
      width: containerWidth,
      height: containerHeight,
      layout: {
        background: { type: ColorType.Solid, color: 'transparent' },
        textColor: '#333',
      },
      grid: {
        vertLines: { color: 'rgba(197, 203, 206, 0.5)' },
        horzLines: { color: 'rgba(197, 203, 206, 0.5)' },
      },
      crosshair: {
        mode: 1,
      },
      rightPriceScale: {
        borderColor: 'rgba(197, 203, 206, 0.8)',
        scaleMargins: {
          top: 0.1,
          bottom: 0.25, // 为成交量预留25%的空间
        },
      },
      timeScale: {
        borderColor: 'rgba(197, 203, 206, 0.8)',
      },
    });
    
    console.log('图表创建成功');
    console.log('chart 对象:', chart);
    console.log('addSeries 方法:', typeof chart.addSeries);
    
    // 添加K线系列 - 使用新的 API
    candlestickSeries = chart.addSeries(CandlestickSeries, {
      upColor: '#26a69a',
      downColor: '#ef5350',
      borderVisible: false,
      wickUpColor: '#26a69a',
      wickDownColor: '#ef5350',
    });
    
    console.log('K线系列添加成功');
    
    // 添加成交量系列 - 使用新的 API
    volumeSeries = chart.addSeries(HistogramSeries, {
      color: '#26a69a',
      priceFormat: {
        type: 'volume',
      },
      priceScaleId: 'volume',
    });
    
    // 设置成交量刻度 - 调整边距以增加与K线的间距
    chart.priceScale('volume').applyOptions({
      scaleMargins: {
        top: 0.82, // 成交量从82%位置开始，只占用底部15%空间
        bottom: 0.03, // 底部留3%边距
      },
    });
    
    console.log('成交量系列添加成功');
    
    // 添加窗口大小监听
    const handleResize = () => {
      if (chart && chartContainer.value) {
        const newWidth = chartContainer.value.clientWidth || containerWidth;
        chart.applyOptions({ width: newWidth });
      }
    };
    
    window.addEventListener('resize', handleResize);
    
    // 设置数据
    updateChartData();
    
    chartReady.value = true;
    console.log('图表初始化完成');
    
    // 保存清理函数
    (chart as any)._cleanup = () => {
      window.removeEventListener('resize', handleResize);
    };
    
  } catch (error: any) {
    console.error('图表初始化失败:', error);
    console.error('错误详情:', error.message);
    console.error('错误堆栈:', error.stack);
  }
};

onMounted(async () => {
  console.log('组件已挂载，开始初始化图表');
  await nextTick();
  initChart();
});

onUnmounted(() => {
  console.log('组件即将卸载，清理图表');
  if (chart) {
    // 清理自定义的事件监听器
    if ((chart as any)._cleanup) {
      (chart as any)._cleanup();
    }
    chart.remove();
    chart = null;
  }
  chartReady.value = false;
});
</script>

<style lang="scss" scoped>
.candlestick-wrapper {
  width: 100%;
  height: 400px;
  position: relative;
  min-width: 400px;
}

.chart-controls {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  padding: 0 4px;
}

.price-info {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  
  .current-price {
    font-size: 18px;
    font-weight: 600;
    margin-bottom: 2px;
    
    &.positive {
      color: #00d4aa;
    }
    
    &.negative {
      color: #ff4976;
    }
  }
  
  .price-change {
    font-size: 12px;
    
    &.positive {
      color: #00d4aa;
    }
    
    &.negative {
      color: #ff4976;
    }
  }
}

.candlestick-chart {
  width: 100%;
  height: 350px;
  min-width: 400px;
  position: relative;
  border-radius: 8px;
  overflow: hidden;
  border: 1px solid #e5e7eb;
  background: #fff;
}

.chart-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: #666;
  font-size: 14px;
}

@media (max-width: 768px) {
  .candlestick-wrapper {
    min-width: 300px;
  }
  
  .candlestick-chart {
    min-width: 300px;
  }
  
  .chart-controls {
    flex-direction: column;
    gap: 8px;
    align-items: stretch;
  }
  
  .price-info {
    align-items: center;
  }
}
</style> 