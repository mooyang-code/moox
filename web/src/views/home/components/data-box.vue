<template>
  <div class="data-box">
    <div class="candlestick-section">
      <div>
        <span class="data-title">股票K线图</span>
        <span class="data-subtext">实时价格走势</span>
      </div>
      <CandlestickChart />
    </div>
    <div class="monthly-analysis">
      <div>
        <span class="data-title">现金分析</span>
        <span class="data-subtext">单位：元</span>
      </div>
      <AnalysisChart />
    </div>
  </div>
</template>

<script setup lang="ts">
import CandlestickChart from "@/views/home/components/candlestick-chart.vue";
import AnalysisChart from "@/views/home/components/analysis-chart.vue";
</script>

<style lang="scss" scoped>
.data-box {
  display: flex;
  flex-wrap: wrap;
  justify-content: space-between;
  margin-top: calc($padding * 2);
  width: 100%;
  .data-title {
    font-size: $font-size-body-3;
    color: $color-text-1;
  }
  .data-subtext {
    margin-left: $margin-text;
    font-size: $font-size-body-2;
    color: $color-text-2;
  }
}

.candlestick-section, .monthly-analysis {
  flex: 1;
  min-width: 450px;
  max-width: calc(50% - 16px);
  margin: 0 8px;
}

.candlestick-section {
  margin-right: 16px;
}

.monthly-analysis {
  margin-left: 16px;
}

@media (max-width: 900px) {
  .candlestick-section, .monthly-analysis {
    margin: 8px 0;
    flex: none;
    width: 100%;
    min-width: 300px;
    max-width: none;
  }
}
</style>
