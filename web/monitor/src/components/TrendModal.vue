<script setup lang="ts">
import { ref } from "vue";
import { fetchTrafficTrend, fetchTrafficRecent } from "../api";
import { formatBytes } from "../utils";
import { buildFrame, lineSeries, bytesAxis, aggregateTrafficDaily, type TimeUnit } from "../chartOptions";
import { tzOffsetMinutes } from "../timezone";
import { useTrendChart } from "../useTrendChart";
import type { SourceSummary, HourlyPoint, TrafficRawPoint } from "../types";

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

type Granularity = "recent" | "hourly" | "daily";
const granularity = ref<Granularity>("hourly");
const trend = ref<HourlyPoint[]>([]);
const recentPoints = ref<TrafficRawPoint[]>([]);

async function load() {
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
    const data = isDaily ? aggregateTrafficDaily(trend.value) : trend.value;
    const showSymbol = !narrow;
    series = [
      lineSeries("Inbound", "#2563eb", data.map((p) => [p.hourTs * 1000, p.inBytes]), { showSymbol }),
      lineSeries("Outbound", "#06b6d4", data.map((p) => [p.hourTs * 1000, p.outBytes]), { showSymbol }),
      lineSeries("Total", "#22c55e", data.map((p) => [p.hourTs * 1000, p.totalBytes]), { showSymbol }),
    ];
  }

  return { ...option, yAxis: bytesAxis(narrow), series };
}

function close() {
  emit("close");
}

const { chartRef, loading } = useTrendChart(load, buildOption, [granularity, tzOffsetMinutes], close);
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
