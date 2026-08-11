<script setup lang="ts">
import { ref } from "vue";
import PeakAverageToggle from "./PeakAverageToggle.vue";
import { fetchIPDetail } from "../api";
import { formatBytes } from "../utils";
import { buildFrame, lineSeries, bytesAxis, withPeakAverage, type TimeUnit } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { IPSeriesPoint, IPTrafficRow } from "../types";

const props = defineProps<{ row: IPTrafficRow; location: string; sources: string[] }>();
const emit = defineEmits<{ close: [] }>();

// The same three granularities the node's own traffic modal offers, reading the
// same three tables underneath, so an address's chart and its node's chart are
// the same measurement at different scopes.
type Granularity = "recent" | "hourly" | "daily";
const granularity = ref<Granularity>("hourly");
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

function buildOption(): any {
  const isRecent = granularity.value === "recent";
  const isDaily = granularity.value === "daily";
  const unit: TimeUnit = isDaily ? "day" : "hour";
  const points = isRecent ? recent.value : isDaily ? daily.value : hourly.value;

  const { narrow, plotHeight, option } = buildFrame({
    width: chartRef.value?.clientWidth ?? 800,
    height: chartRef.value?.clientHeight ?? 0,
    unit,
    legend: ["Inbound", "Outbound", "Total"],
    tooltipUnit: isRecent ? "second" : unit,
    tooltipValue: (p) => formatBytes(Array.isArray(p.value) ? p.value[1] : p.value),
  });

  const showSymbol = !narrow && !isRecent;
  const series = [
    lineSeries("Inbound", "#2563eb", points.map((p) => [p.ts * 1000, p.inBytes]), { showSymbol }),
    lineSeries("Outbound", "#06b6d4", points.map((p) => [p.ts * 1000, p.outBytes]), { showSymbol }),
    lineSeries("Total", "#22c55e", points.map((p) => [p.ts * 1000, p.totalBytes]), { showSymbol }),
  ];

  return {
    ...option,
    yAxis: bytesAxis(narrow),
    series: withPeakAverage(series, { show: showPeakAverage.value,
      plotHeight, format: formatBytes, narrow }),
  };
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [granularity, tzOffsetMinutes], close, [showPeakAverage]);
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <button class="close-btn" @click="close" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ row.ip }}</h2>
          <p class="modal-subtitle">
            {{ location || "Location unresolved" }}
            <span v-if="row.nodes.length"> · {{ row.nodes.join(", ") }}</span>
          </p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button :class="{ active: granularity === 'recent' }" @click="granularity = 'recent'">Recent</button>
            <button :class="{ active: granularity === 'hourly' }" @click="granularity = 'hourly'">Hourly</button>
            <button :class="{ active: granularity === 'daily' }" @click="granularity = 'daily'">Daily</button>
          </div>
          <PeakAverageToggle v-model="showPeakAverage" />
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
