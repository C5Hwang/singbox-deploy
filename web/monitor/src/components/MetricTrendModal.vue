<script setup lang="ts">
import { ref } from "vue";
import { fetchTrafficTrend, fetchTrafficRecent, fetchResourceTrend, fetchResourceRecent } from "../api";
import { formatBytes } from "../utils";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import {
  buildFrame,
  lineSeries,
  bytesAxis,
  percentAxis,
  aggregateTrafficDaily,
  aggregateResourceDaily,
  withPeakAverage,
  SOURCE_COLORS,
  type TimeUnit,
} from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type {
  MetricDef,
  SourceSummary,
  HourlyPoint,
  ResourceHourlyPoint,
  TrafficRawPoint,
  ResourceRawPoint,
} from "../types";

const props = defineProps<{ metric: MetricDef; sources: SourceSummary[] }>();
const emit = defineEmits<{ close: [] }>();

const isTraffic = props.metric.kind === "traffic";

type Mode = "recent" | "hourly" | "daily" | "hourly-avg" | "hourly-max" | "daily-avg" | "daily-max";
const modes: { key: Mode; label: string }[] = isTraffic
  ? [
      { key: "recent", label: "Recent" },
      { key: "hourly", label: "Hourly" },
      { key: "daily", label: "Daily" },
    ]
  : [
      { key: "recent", label: "Recent" },
      { key: "hourly-avg", label: "Hourly (Avg)" },
      { key: "hourly-max", label: "Hourly (Max)" },
      { key: "daily-avg", label: "Daily (Avg)" },
      { key: "daily-max", label: "Daily (Max)" },
    ];
const mode = ref<Mode>(isTraffic ? "hourly" : "hourly-avg");
const showPeakAverage = ref(false);

interface MachineSeries {
  name: string;
  trend: (HourlyPoint | ResourceHourlyPoint)[];
  recent: (TrafficRawPoint | ResourceRawPoint)[];
}
const machines = ref<MachineSeries[]>([]);

// One request pair per machine, in parallel; a failed source just contributes
// an empty line instead of blanking the whole chart.
async function load() {
  machines.value = await Promise.all(
    props.sources.map(async (source) => {
      try {
        const [trend, recent] = isTraffic
          ? await Promise.all([fetchTrafficTrend(source.id || source.name), fetchTrafficRecent(source.id || source.name)])
          : await Promise.all([fetchResourceTrend(source.id || source.name), fetchResourceRecent(source.id || source.name)]);
        return { name: source.name, trend, recent };
      } catch {
        return { name: source.name, trend: [], recent: [] };
      }
    }),
  );
}

function recentValue(p: TrafficRawPoint | ResourceRawPoint): number {
  if (props.metric.kind === "traffic") return (p as TrafficRawPoint)[props.metric.key];
  const rp = p as ResourceRawPoint;
  if (props.metric.key === "cpu") return rp.cpuPct;
  if (props.metric.key === "mem") return rp.memPct;
  return rp.diskPct;
}

function hourlyValue(p: HourlyPoint | ResourceHourlyPoint, isMax: boolean): number {
  if (props.metric.kind === "traffic") return (p as HourlyPoint)[props.metric.key];
  const rp = p as ResourceHourlyPoint;
  if (props.metric.key === "cpu") return isMax ? rp.cpuMax : rp.cpuAvg;
  if (props.metric.key === "mem") return isMax ? rp.memMax : rp.memAvg;
  return isMax ? rp.diskMax : rp.diskAvg;
}

function buildOption(): any {
  const isRecent = mode.value === "recent";
  const isDaily = mode.value.startsWith("daily");
  const isMax = mode.value.endsWith("max");
  const unit: TimeUnit = isDaily ? "day" : "hour";

  const { narrow, plotHeight, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    height: chartRef.value?.clientHeight ?? 0,
    unit,
    legend: machines.value.map((m) => m.name),
    sortTooltip: true,
    tooltipUnit: isRecent ? "second" : unit,
    tooltipValue: (p) => {
      const value = Array.isArray(p.value) ? p.value[1] : p.value;
      if (isTraffic) return formatBytes(value);
      const n = Number(value);
      return Number.isFinite(n) ? `${n.toFixed(1)}%` : "NA";
    },
  });

  const showSymbol = !narrow;
  const series = machines.value.map((m, i) => {
    const color = SOURCE_COLORS[i % SOURCE_COLORS.length];
    if (isRecent) {
      return lineSeries(m.name, color, m.recent.map((p) => [p.ts * 1000, recentValue(p)]));
    }
    let points = m.trend;
    if (isDaily) {
      points = isTraffic
        ? aggregateTrafficDaily(m.trend as HourlyPoint[])
        : aggregateResourceDaily(m.trend as ResourceHourlyPoint[], isMax);
    }
    return lineSeries(
      m.name,
      color,
      points.map((p) => [p.hourTs * 1000, hourlyValue(p, isMax)]),
      { showSymbol },
    );
  });

  const format = (v: number) => (isTraffic ? formatBytes(v) : `${v.toFixed(1)}%`);
  return {
    ...option,
    yAxis: isTraffic ? bytesAxis(narrow) : percentAxis(narrow),
    series: withPeakAverage(series, { show: showPeakAverage.value,
      plotHeight, format, narrow }),
  };
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
          <h2 class="modal-title">{{ metric.title }}</h2>
          <p class="modal-subtitle">All Sources · {{ sources.length }} machine{{ sources.length > 1 ? "s" : "" }}</p>
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
