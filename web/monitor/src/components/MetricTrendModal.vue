<script setup lang="ts">
import { ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { fetchTrafficTrend, fetchTrafficRecent, fetchResourceTrend, fetchResourceRecent } from "../api";
import { formatBytes } from "../utils";
import {
  aggregateTrafficDaily,
  aggregateResourceDaily,
  buildTrendOption,
  bytesAxis,
  lineSeries,
  percentAxis,
  SOURCE_COLORS,
} from "../chartOptions";
import { modeShape, RESOURCE_MODES, TRAFFIC_MODES, type TrendMode } from "../trendModes";
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

// One metric across every machine, at the granularities that metric has: a
// total for traffic, an average and a peak for a resource.
const modes = isTraffic ? TRAFFIC_MODES : RESOURCE_MODES;
const mode = ref<TrendMode>(isTraffic ? "hourly" : "hourly-avg");
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

function formatValue(value: number): string {
  if (isTraffic) return formatBytes(value);
  return Number.isFinite(value) ? `${value.toFixed(1)}%` : "NA";
}

function buildOption(): any {
  const { isRecent, isDaily, isMax, unit, tooltipUnit } = modeShape(mode.value);

  return buildTrendOption({
    el: chartRef.value,
    unit,
    tooltipUnit,
    legend: machines.value.map((m) => m.name),
    // The question this chart answers is which machine is highest, so the
    // tooltip ranks them rather than listing them in fleet order.
    sortTooltip: true,
    tooltipValue: (p) => formatValue(Number(Array.isArray(p.value) ? p.value[1] : p.value)),
    yAxis: isTraffic ? bytesAxis : percentAxis,
    series: (narrow) =>
      machines.value.map((m, i) => {
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
          { showSymbol: !narrow },
        );
      }),
    peakAverage: { show: showPeakAverage.value, format: formatValue },
  });
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [tzOffsetMinutes], close, [showPeakAverage], [mode]);
</script>

<template>
  <TrendShell
    :title="metric.title"
    :subtitle="`All Sources · ${sources.length} machine${sources.length > 1 ? 's' : ''}`"
    :modes="modes"
    v-model:mode="mode"
    v-model:peakAverage="showPeakAverage"
    :loading="loading"
    @close="close"
  >
    <div ref="chartRef" class="chart-container"></div>
  </TrendShell>
</template>
