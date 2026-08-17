<script setup lang="ts">
import { ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { fetchResourceTrend, fetchResourceRecent } from "../api";
import { formatRate } from "../utils";
import {
  aggregateResourceDaily,
  buildTrendOption,
  lineSeries,
  percentAxis,
  rateAxis,
} from "../chartOptions";
import { modeShape, RESOURCE_MODES, type TrendMode } from "../trendModes";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { SourceSummary, ResourceHourlyPoint, ResourceRawPoint } from "../types";

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const mode = ref<TrendMode>("hourly-avg");
const showPeakAverage = ref(false);
const trend = ref<ResourceHourlyPoint[]>([]);
const recentPoints = ref<ResourceRawPoint[]>([]);

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

// Two of the four lines are byte rates against their own axis while the other
// two are percentages, so every label on this chart — tooltip row, average
// chip, peak chip — is asked which line it belongs to before it is written.
const RATE_SERIES = ["Disk IO Read", "Disk IO Write"];

function isRate(name: string): boolean {
  return RATE_SERIES.includes(name);
}

function formatValue(value: number, name: string): string {
  if (isRate(name)) return formatRate(value);
  return Number.isFinite(value) ? `${value.toFixed(1)}%` : "NA";
}

function buildOption(): any {
  const { isRecent, isDaily, isMax, unit, tooltipUnit } = modeShape(mode.value);

  return buildTrendOption({
    el: chartRef.value,
    unit,
    tooltipUnit,
    legend: ["CPU %", "Memory %", "Disk IO Read", "Disk IO Write"],
    tooltipValue: (p) => formatValue(Number(Array.isArray(p.value) ? p.value[1] : p.value), p.seriesName),
    yAxis: (narrow) => [percentAxis(narrow), rateAxis(narrow)],
    series: (narrow) => {
      if (isRecent) {
        const data = recentPoints.value;
        return [
          lineSeries("CPU %", "#2563eb", data.map((p) => [p.ts * 1000, p.cpuPct])),
          lineSeries("Memory %", "#06b6d4", data.map((p) => [p.ts * 1000, p.memPct])),
          lineSeries("Disk IO Read", "#22c55e", data.map((p) => [p.ts * 1000, p.dioRead]), { yAxisIndex: 1 }),
          lineSeries("Disk IO Write", "#f59e0b", data.map((p) => [p.ts * 1000, p.dioWrite]), { yAxisIndex: 1 }),
        ];
      }
      const data = isDaily ? aggregateResourceDaily(trend.value, isMax) : trend.value;
      const cpuKey = isMax ? "cpuMax" : "cpuAvg";
      const memKey = isMax ? "memMax" : "memAvg";
      const readKey = isMax ? "dioReadMax" : "dioReadAvg";
      const writeKey = isMax ? "dioWriteMax" : "dioWriteAvg";
      const showSymbol = !narrow;
      return [
        lineSeries("CPU %", "#2563eb", data.map((p) => [p.hourTs * 1000, p[cpuKey]]), { showSymbol }),
        lineSeries("Memory %", "#06b6d4", data.map((p) => [p.hourTs * 1000, p[memKey]]), { showSymbol }),
        lineSeries("Disk IO Read", "#22c55e", data.map((p) => [p.hourTs * 1000, p[readKey]]), { yAxisIndex: 1, showSymbol }),
        lineSeries("Disk IO Write", "#f59e0b", data.map((p) => [p.hourTs * 1000, p[writeKey]]), { yAxisIndex: 1, showSymbol }),
      ];
    },
    peakAverage: { show: showPeakAverage.value, format: (value, series) => formatValue(value, series.name) },
  });
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [mode, tzOffsetMinutes], close, [showPeakAverage]);
</script>

<template>
  <TrendShell
    :title="source.name"
    subtitle="Resource Trend"
    :modes="RESOURCE_MODES"
    v-model:mode="mode"
    v-model:peakAverage="showPeakAverage"
    :loading="loading"
    @close="close"
  >
    <div ref="chartRef" class="chart-container"></div>
  </TrendShell>
</template>
