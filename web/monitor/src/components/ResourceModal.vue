<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, shallowRef, nextTick } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { fetchResourceTrend, fetchResourceRecent } from "../api";
import { formatRate } from "../utils";
import { buildFrame, lineSeries, percentAxis, rateAxis, type TimeUnit } from "../chartOptions";
import type { SourceSummary, ResourceHourlyPoint, ResourceRawPoint } from "../types";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer]);

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const chartRef = ref<HTMLDivElement>();
const chart = shallowRef<echarts.ECharts>();
type Mode = "recent" | "hourly-avg" | "hourly-max" | "daily-avg" | "daily-max";
const mode = ref<Mode>("hourly-avg");
const trend = ref<ResourceHourlyPoint[]>([]);
const recentPoints = ref<ResourceRawPoint[]>([]);
const loading = ref(true);

const modes: { key: Mode; label: string }[] = [
  { key: "recent", label: "Recent" },
  { key: "hourly-avg", label: "Hourly (Avg)" },
  { key: "hourly-max", label: "Hourly (Max)" },
  { key: "daily-avg", label: "Daily (Avg)" },
  { key: "daily-max", label: "Daily (Max)" },
];

function avg(arr: number[]): number {
  if (arr.length === 0) return 0;
  return arr.reduce((a, b) => a + b, 0) / arr.length;
}

function agg(arr: number[], isMax: boolean): number {
  if (arr.length === 0) return 0;
  return isMax ? Math.max(...arr) : avg(arr);
}

function formatTooltipValue(param: any): string {
  const value = Array.isArray(param.value) ? param.value[1] : param.value;
  if (param.seriesName === "Disk IO Read" || param.seriesName === "Disk IO Write") return formatRate(value);
  const numberValue = Number(value);
  return Number.isFinite(numberValue) ? `${numberValue.toFixed(1)}%` : "NA";
}

function aggregateDaily(points: ResourceHourlyPoint[], isMax: boolean): ResourceHourlyPoint[] {
  const buckets = new Map<number, ResourceHourlyPoint[]>();
  for (const p of points) {
    const dayTs = Math.floor(p.hourTs / 86400) * 86400;
    if (!buckets.has(dayTs)) buckets.set(dayTs, []);
    buckets.get(dayTs)!.push(p);
  }
  return Array.from(buckets.entries())
    .sort(([a], [b]) => a - b)
    .map(([dayTs, pts]) => {
      const v = (key: keyof ResourceHourlyPoint) => pts.map((p) => p[key] as number);
      return {
        hourTs: dayTs,
        cpuAvg: agg(v("cpuAvg"), isMax),
        cpuMax: agg(v("cpuMax"), isMax),
        memAvg: agg(v("memAvg"), isMax),
        memMax: agg(v("memMax"), isMax),
        diskAvg: agg(v("diskAvg"), isMax),
        diskMax: agg(v("diskMax"), isMax),
        dioReadAvg: agg(v("dioReadAvg"), isMax),
        dioReadMax: agg(v("dioReadMax"), isMax),
        dioWriteAvg: agg(v("dioWriteAvg"), isMax),
        dioWriteMax: agg(v("dioWriteMax"), isMax),
      };
    });
}

function buildOption(): any {
  const isRecent = mode.value === "recent";
  const isDaily = mode.value.startsWith("daily");
  const unit: TimeUnit = isDaily ? "day" : "hour";

  const { narrow, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    unit,
    legend: ["CPU %", "Memory %", "Disk IO Read", "Disk IO Write"],
    tooltipUnit: isRecent ? "second" : unit,
    tooltipValue: formatTooltipValue,
  });

  let series;
  if (isRecent) {
    const data = recentPoints.value;
    series = [
      lineSeries("CPU %", "#2563eb", data.map((p) => [p.ts * 1000, p.cpuPct])),
      lineSeries("Memory %", "#06b6d4", data.map((p) => [p.ts * 1000, p.memPct])),
      lineSeries("Disk IO Read", "#22c55e", data.map((p) => [p.ts * 1000, p.dioRead]), { yAxisIndex: 1 }),
      lineSeries("Disk IO Write", "#f59e0b", data.map((p) => [p.ts * 1000, p.dioWrite]), { yAxisIndex: 1 }),
    ];
  } else {
    const isMax = mode.value.endsWith("max");
    const data = isDaily ? aggregateDaily(trend.value, isMax) : trend.value;
    const cpuKey = isMax ? "cpuMax" : "cpuAvg";
    const memKey = isMax ? "memMax" : "memAvg";
    const readKey = isMax ? "dioReadMax" : "dioReadAvg";
    const writeKey = isMax ? "dioWriteMax" : "dioWriteAvg";
    const showSymbol = !narrow;
    series = [
      lineSeries("CPU %", "#2563eb", data.map((p) => [p.hourTs * 1000, (p as any)[cpuKey]]), { showSymbol }),
      lineSeries("Memory %", "#06b6d4", data.map((p) => [p.hourTs * 1000, (p as any)[memKey]]), { showSymbol }),
      lineSeries("Disk IO Read", "#22c55e", data.map((p) => [p.hourTs * 1000, (p as any)[readKey]]), { yAxisIndex: 1, showSymbol }),
      lineSeries("Disk IO Write", "#f59e0b", data.map((p) => [p.hourTs * 1000, (p as any)[writeKey]]), { yAxisIndex: 1, showSymbol }),
    ];
  }

  return { ...option, yAxis: [percentAxis(narrow), rateAxis(narrow)], series };
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
      fetchResourceTrend(props.source.name),
      fetchResourceRecent(props.source.name),
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

watch(mode, () => {
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
          <p class="modal-subtitle">Resource Trend</p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button v-for="m in modes" :key="m.key" :class="{ active: mode === m.key }" @click="mode = m.key">{{ m.label }}</button>
          </div>
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
