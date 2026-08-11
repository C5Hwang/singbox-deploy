<script setup lang="ts">
import { ref } from "vue";
import { fetchResourceTrend, fetchResourceRecent } from "../api";
import { formatRate } from "../utils";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { buildFrame, lineSeries, percentAxis, rateAxis, aggregateResourceDaily, withPeakAverage, type TimeUnit } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { SourceSummary, ResourceHourlyPoint, ResourceRawPoint } from "../types";

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

type Mode = "recent" | "hourly-avg" | "hourly-max" | "daily-avg" | "daily-max";
const mode = ref<Mode>("hourly-avg");
const showPeakAverage = ref(false);
const trend = ref<ResourceHourlyPoint[]>([]);
const recentPoints = ref<ResourceRawPoint[]>([]);

const modes: { key: Mode; label: string }[] = [
  { key: "recent", label: "Recent" },
  { key: "hourly-avg", label: "Hourly (Avg)" },
  { key: "hourly-max", label: "Hourly (Max)" },
  { key: "daily-avg", label: "Daily (Avg)" },
  { key: "daily-max", label: "Daily (Max)" },
];

async function load() {
  try {
    const [trendData, recentData] = await Promise.all([
      fetchResourceTrend(props.source.id || props.source.name),
      fetchResourceRecent(props.source.id || props.source.name),
    ]);
    trend.value = trendData;
    recentPoints.value = recentData;
  } catch {
    trend.value = [];
    recentPoints.value = [];
  }
}

function formatTooltipValue(param: any): string {
  const value = Array.isArray(param.value) ? param.value[1] : param.value;
  if (param.seriesName === "Disk IO Read" || param.seriesName === "Disk IO Write") return formatRate(value);
  const numberValue = Number(value);
  return Number.isFinite(numberValue) ? `${numberValue.toFixed(1)}%` : "NA";
}

function buildOption(): any {
  const isRecent = mode.value === "recent";
  const isDaily = mode.value.startsWith("daily");
  const unit: TimeUnit = isDaily ? "day" : "hour";

  const { narrow, plotHeight, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    height: chartRef.value?.clientHeight ?? 0,
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
    const data = isDaily ? aggregateResourceDaily(trend.value, isMax) : trend.value;
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

  // Two axes share this chart, so the overlay labels each series in the unit
  // that series is drawn in rather than in one unit for the whole chart.
  const format = (v: number) => `${v.toFixed(1)}%`;
  const marked = [
    ...withPeakAverage(series.slice(0, 2), { show: showPeakAverage.value,
      plotHeight, format, narrow }),
    ...withPeakAverage(series.slice(2), { show: showPeakAverage.value,
      plotHeight, format: formatRate, narrow }),
  ];
  return { ...option, yAxis: [percentAxis(narrow), rateAxis(narrow)], series: marked };
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [mode, tzOffsetMinutes], close, [showPeakAverage]);
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
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
