<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, shallowRef, nextTick } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { fetchTrafficTrend, fetchTrafficRecent } from "../api";
import { formatBytes } from "../utils";
import { buildFrame, lineSeries, bytesAxis, type TimeUnit } from "../chartOptions";
import type { SourceSummary, HourlyPoint, TrafficRawPoint } from "../types";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer]);

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const chartRef = ref<HTMLDivElement>();
const chart = shallowRef<echarts.ECharts>();
type Granularity = "recent" | "hourly" | "daily";
const granularity = ref<Granularity>("hourly");
const trend = ref<HourlyPoint[]>([]);
const recentPoints = ref<TrafficRawPoint[]>([]);
const loading = ref(true);

function aggregateDaily(points: HourlyPoint[]): HourlyPoint[] {
  const buckets = new Map<number, HourlyPoint>();
  for (const p of points) {
    const dayTs = Math.floor(p.hourTs / 86400) * 86400;
    const existing = buckets.get(dayTs);
    if (existing) {
      existing.inBytes += p.inBytes;
      existing.outBytes += p.outBytes;
      existing.totalBytes += p.totalBytes;
    } else {
      buckets.set(dayTs, { ...p, hourTs: dayTs });
    }
  }
  return Array.from(buckets.values()).sort((a, b) => a.hourTs - b.hourTs);
}

function buildOption(): any {
  const isRecent = granularity.value === "recent";
  const isDaily = granularity.value === "daily";
  const unit: TimeUnit = isDaily ? "day" : "hour";

  const { narrow, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    unit,
    legend: ["Inbound", "Outbound", "Total"],
    tooltipUnit: isRecent ? "second" : unit,
    tooltipValue: (p) => formatBytes(Array.isArray(p.value) ? p.value[1] : p.value),
  });

  let series;
  if (isRecent) {
    const data = recentPoints.value;
    series = [
      lineSeries("Inbound", "#2563eb", data.map((p) => [p.ts * 1000, p.inBytes])),
      lineSeries("Outbound", "#06b6d4", data.map((p) => [p.ts * 1000, p.outBytes])),
      lineSeries("Total", "#22c55e", data.map((p) => [p.ts * 1000, p.totalBytes])),
    ];
  } else {
    const data = isDaily ? aggregateDaily(trend.value) : trend.value;
    const showSymbol = !narrow;
    series = [
      lineSeries("Inbound", "#2563eb", data.map((p) => [p.hourTs * 1000, p.inBytes]), { showSymbol }),
      lineSeries("Outbound", "#06b6d4", data.map((p) => [p.hourTs * 1000, p.outBytes]), { showSymbol }),
      lineSeries("Total", "#22c55e", data.map((p) => [p.hourTs * 1000, p.totalBytes]), { showSymbol }),
    ];
  }

  return { ...option, yAxis: bytesAxis(narrow), series };
}

let resizeHandler: (() => void) | undefined;
let resizeTimer: number | undefined;
let keyHandler: ((e: KeyboardEvent) => void) | undefined;

onMounted(async () => {
  keyHandler = (e) => {
    if (e.key === "Escape") close();
  };
  window.addEventListener("keydown", keyHandler);
  try {
    const [trendData, recentData] = await Promise.all([
      fetchTrafficTrend(props.source.name),
      fetchTrafficRecent(props.source.name),
    ]);
    trend.value = trendData;
    recentPoints.value = recentData;
  } catch {
    trend.value = [];
    recentPoints.value = [];
  }
  loading.value = false;
  await nextTick();
  if (chartRef.value) {
    chart.value = echarts.init(chartRef.value);
    chart.value.setOption(buildOption());
    resizeHandler = () => {
      if (resizeTimer) window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(() => {
        chart.value?.resize();
        chart.value?.setOption(buildOption(), true);
      }, 120);
    };
    window.addEventListener("resize", resizeHandler);
  }
});

onUnmounted(() => {
  if (resizeHandler) window.removeEventListener("resize", resizeHandler);
  if (keyHandler) window.removeEventListener("keydown", keyHandler);
  if (resizeTimer) window.clearTimeout(resizeTimer);
  chart.value?.dispose();
});

watch(granularity, () => {
  chart.value?.setOption(buildOption(), true);
});

function close() {
  emit("close");
}
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ source.name }}</h2>
          <p class="modal-subtitle">Traffic Trend</p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button :class="{ active: granularity === 'recent' }" @click="granularity = 'recent'">Recent</button>
            <button :class="{ active: granularity === 'hourly' }" @click="granularity = 'hourly'">Hourly</button>
            <button :class="{ active: granularity === 'daily' }" @click="granularity = 'daily'">Daily</button>
          </div>
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
