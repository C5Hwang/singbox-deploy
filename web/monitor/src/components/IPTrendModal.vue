<script setup lang="ts">
import { computed, ref } from "vue";
import TrendShell from "./TrendShell.vue";
import { fetchIPDetail, ipDetailKey } from "../api";
import { formatBytes } from "../utils";
import { buildTrendOption, bytesAxis, trafficSeries, TRAFFIC_LEGEND, type TrafficPoint } from "../chartOptions";
import { modeShape, TRAFFIC_MODES, type TrendMode } from "../trendModes";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { IPSeriesPoint, IPTrafficRow, IPTrafficSegment } from "../types";

// segment narrows the chart to one strand of the row — the address's direct
// traffic, or what the fleet relayed for it to one landing node. Null charts
// the row whole, which is every strand summed.
const props = defineProps<{
  row: IPTrafficRow;
  segment?: IPTrafficSegment | null;
  location: string;
  sources: string[];
}>();
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

// Each strand is stored under its own key, so the row as a whole is charted by
// asking for all of them and summing — the same arithmetic the table does on
// the numbers, applied to the buckets behind them.
const keys = computed(() => {
  const strands = props.segment ? [props.segment] : props.row.segments;
  return strands.map((s) => ipDetailKey({ ip: props.row.ip, relayed: s.relayed, landing: s.landing }));
});

async function load() {
  const requests = keys.value.flatMap((key) =>
    props.sources.map(async (source) => {
      try {
        return await fetchIPDetail(key, source);
      } catch {
        return null;
      }
    }),
  );
  const details = await Promise.all(requests);
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
  const strand = props.segment;
  const nodes = strand ? strand.nodes : props.row.nodes;
  const parts = [props.location || "Location unresolved"];
  if (nodes.length) parts.push(nodes.join(", "));
  if (strand) parts.push(strand.relayed ? `relayed to ${strand.label}` : "direct");
  else if (props.row.relayed) parts.push("direct and relayed");
  return parts.join(" · ");
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

const { chartRef, loading } = useTrendChart(load, buildOption, [tzOffsetMinutes], close, [showPeakAverage], [mode]);
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
