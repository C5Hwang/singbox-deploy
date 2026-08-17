<script setup lang="ts">
import { computed, ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { fetchIPDetail } from "../api";
import { formatBytes } from "../utils";
import { buildTrendOption, bytesAxis, trafficSeries, TRAFFIC_LEGEND, type TrafficPoint } from "../chartOptions";
import { modeShape, TRAFFIC_MODES, type TrendMode } from "../trendModes";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { IPSeriesPoint, IPTrafficRow } from "../types";

const props = defineProps<{ row: IPTrafficRow; location: string; sources: string[] }>();
const emit = defineEmits<{ close: [] }>();

// The same three granularities the node's own traffic modal offers, reading the
// same three tables underneath, so an address's chart and its node's chart are
// the same measurement at different scopes.
const mode = ref<TrendMode>("hourly");
const showPeakAverage = ref(false);

const recent = ref<IPSeriesPoint[]>([]);
const hourly = ref<IPSeriesPoint[]>([]);
const daily = ref<IPSeriesPoint[]>([]);

// The table merges an address across nodes, so its chart does too: every node
// the row covers is queried and the buckets are summed by timestamp.
function mergeSeries(all: IPSeriesPoint[][]): IPSeriesPoint[] {
  const byTs = new Map<number, IPSeriesPoint>();
  for (const series of all) {
    for (const point of series) {
      const existing = byTs.get(point.ts);
      if (existing) {
        existing.inBytes += point.inBytes;
        existing.outBytes += point.outBytes;
        existing.totalBytes += point.totalBytes;
      } else {
        byTs.set(point.ts, { ...point });
      }
    }
  }
  return [...byTs.values()].sort((a, b) => a.ts - b.ts);
}

async function load() {
  const details = await Promise.all(
    props.sources.map(async (source) => {
      try {
        return await fetchIPDetail(props.row.ip, source);
      } catch {
        return null;
      }
    }),
  );
  const present = details.filter((d) => d !== null);
  recent.value = mergeSeries(present.map((d) => d.recent));
  hourly.value = mergeSeries(present.map((d) => d.hourly));
  daily.value = mergeSeries(present.map((d) => d.daily));
}

// The address tables are stored one per granularity rather than rolled up, so
// picking the mode is picking the table.
const points = computed<TrafficPoint[]>(() => {
  const { isRecent, isDaily } = modeShape(mode.value);
  return isRecent ? recent.value : isDaily ? daily.value : hourly.value;
});

const subtitle = computed(() => {
  const place = props.location || "Location unresolved";
  return props.row.nodes.length ? `${place} · ${props.row.nodes.join(", ")}` : place;
});

function buildOption(): any {
  const { isRecent, unit, tooltipUnit } = modeShape(mode.value);
  return buildTrendOption({
    el: chartRef.value,
    unit,
    tooltipUnit,
    legend: TRAFFIC_LEGEND,
    tooltipValue: (p) => formatBytes(Array.isArray(p.value) ? p.value[1] : p.value),
    yAxis: bytesAxis,
    series: (narrow) => trafficSeries(points.value, !narrow && !isRecent),
    peakAverage: { show: showPeakAverage.value, format: formatBytes },
  });
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [mode, tzOffsetMinutes], close, [showPeakAverage]);
</script>

<template>
  <TrendShell
    :title="row.ip"
    :subtitle="subtitle"
    :modes="TRAFFIC_MODES"
    v-model:mode="mode"
    v-model:peakAverage="showPeakAverage"
    :loading="loading"
    @close="close"
  >
    <div ref="chartRef" class="chart-container"></div>
  </TrendShell>
</template>
