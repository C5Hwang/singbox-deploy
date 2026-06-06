<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, shallowRef, nextTick } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { fetchResourceTrend } from "../api";
import { formatBytes } from "../utils";
import type { SourceSummary, ResourceHourlyPoint } from "../types";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer]);

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const chartRef = ref<HTMLDivElement>();
const chart = shallowRef<echarts.ECharts>();
type Mode = "hourly-avg" | "hourly-max" | "daily-avg" | "daily-max";
const mode = ref<Mode>("hourly-avg");
const trend = ref<ResourceHourlyPoint[]>([]);
const loading = ref(true);

const modes: { key: Mode; label: string }[] = [
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

function buildOption(points: ResourceHourlyPoint[]): any {
  const isDaily = mode.value.startsWith("daily");
  const isMax = mode.value.endsWith("max");
  const data = isDaily ? aggregateDaily(points, isMax) : points;

  const cpuKey = isMax ? "cpuMax" : "cpuAvg";
  const memKey = isMax ? "memMax" : "memAvg";
  const readKey = isMax ? "dioReadMax" : "dioReadAvg";
  const writeKey = isMax ? "dioWriteMax" : "dioWriteAvg";

  return {
    animation: true,
    animationDuration: 800,
    animationEasing: "cubicInOut",
    tooltip: {
      trigger: "axis",
      backgroundColor: "rgba(255,255,255,0.96)",
      borderColor: "#e7ecf4",
      textStyle: { color: "#172033", fontSize: 13 },
      formatter(params: any) {
        if (!Array.isArray(params) || params.length === 0) return "";
        const d = new Date(params[0].value[0]);
        const timeStr = isDaily
          ? d.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric", timeZone: "UTC" })
          : d.toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" }) + " GMT";
        let html = `<div style="font-weight:700;margin-bottom:6px">${timeStr}</div>`;
        for (const p of params) {
          const val = p.yAxisIndex === 1 ? `${formatBytes(p.value[1])}/s` : `${p.value[1].toFixed(1)}%`;
          html += `<div style="display:flex;align-items:center;gap:6px;margin:3px 0">`;
          html += `<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:${p.color}"></span>`;
          html += `<span>${p.seriesName}: <b>${val}</b></span></div>`;
        }
        return html;
      },
    },
    legend: {
      data: ["CPU %", "Memory %", "Disk IO Read", "Disk IO Write"],
      bottom: 50,
      itemGap: 20,
      textStyle: { fontSize: 13, fontWeight: 600 },
    },
    grid: { left: 60, right: 70, top: 30, bottom: 100 },
    xAxis: {
      type: "time",
      axisLine: { lineStyle: { color: "#e7ecf4" } },
      axisLabel: {
        color: "#7a869a",
        fontSize: 12,
        formatter(value: number) {
          const d = new Date(value);
          return isDaily
            ? d.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })
            : d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", timeZone: "UTC", hour12: false });
        },
      },
    },
    yAxis: [
      {
        type: "value",
        name: "%",
        min: 0,
        max: 100,
        position: "left",
        axisLine: { show: false },
        splitLine: { lineStyle: { color: "#f0f4f8" } },
        axisLabel: { color: "#7a869a", fontSize: 12, formatter: (v: number) => `${v}%` },
      },
      {
        type: "value",
        name: "IO",
        position: "right",
        axisLine: { show: false },
        splitLine: { show: false },
        axisLabel: {
          color: "#7a869a",
          fontSize: 12,
          formatter: (v: number) => `${formatBytes(v)}/s`,
        },
      },
    ],
    dataZoom: [
      {
        type: "slider",
        show: true,
        left: 60,
        right: 70,
        bottom: 10,
        height: 28,
        borderColor: "transparent",
        backgroundColor: "#f0f4f8",
        fillerColor: "rgba(37, 99, 235, 0.12)",
        handleStyle: { color: "#2563eb", borderColor: "#2563eb" },
        dataBackground: {
          areaStyle: { color: "rgba(37, 99, 235, 0.06)" },
          lineStyle: { color: "rgba(37, 99, 235, 0.2)" },
        },
        selectedDataBackground: {
          areaStyle: { color: "rgba(37, 99, 235, 0.12)" },
          lineStyle: { color: "rgba(37, 99, 235, 0.4)" },
        },
        textStyle: { fontSize: 11, color: "#7a869a" },
      },
      { type: "inside" },
    ],
    series: [
      {
        name: "CPU %",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        yAxisIndex: 0,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#2563eb" },
        data: data.map((p) => [p.hourTs * 1000, (p as any)[cpuKey]]),
      },
      {
        name: "Memory %",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        yAxisIndex: 0,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#06b6d4" },
        data: data.map((p) => [p.hourTs * 1000, (p as any)[memKey]]),
      },
      {
        name: "Disk IO Read",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        yAxisIndex: 1,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#22c55e" },
        data: data.map((p) => [p.hourTs * 1000, (p as any)[readKey]]),
      },
      {
        name: "Disk IO Write",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        yAxisIndex: 1,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#f59e0b" },
        data: data.map((p) => [p.hourTs * 1000, (p as any)[writeKey]]),
      },
    ],
  };
}

let resizeHandler: (() => void) | undefined;

onMounted(async () => {
  try {
    trend.value = await fetchResourceTrend(props.source.name);
  } catch {
    trend.value = [];
  }
  loading.value = false;
  await nextTick();
  if (chartRef.value) {
    chart.value = echarts.init(chartRef.value);
    chart.value.setOption(buildOption(trend.value));
    resizeHandler = () => chart.value?.resize();
    window.addEventListener("resize", resizeHandler);
  }
});

onUnmounted(() => {
  if (resizeHandler) window.removeEventListener("resize", resizeHandler);
  chart.value?.dispose();
});

watch(mode, () => {
  chart.value?.setOption(buildOption(trend.value), true);
});

function close() {
  emit("close");
}
</script>

<template>
  <div class="modal-backdrop" @click.self="close">
    <div class="modal-content">
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ source.name }}</h2>
          <p class="modal-subtitle">Resource Trend</p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button v-for="m in modes" :key="m.key" :class="{ active: mode === m.key }" @click="mode = m.key">{{ m.label }}</button>
          </div>
          <button class="close-btn" @click="close" aria-label="Close">&times;</button>
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
