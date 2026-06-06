<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch, shallowRef, nextTick } from "vue";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, DataZoomComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { fetchTrafficTrend, fetchTrafficRecent } from "../api";
import { formatBytes } from "../utils";
import type { SourceSummary, HourlyPoint, TrafficRawPoint } from "../types";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, DataZoomComponent, CanvasRenderer]);

const props = defineProps<{ source: SourceSummary }>();
const emit = defineEmits<{ close: [] }>();

const chartRef = ref<HTMLDivElement>();
const chart = shallowRef<echarts.ECharts>();
type Granularity = "recent" | "hourly" | "daily";
const granularity = ref<Granularity>("hourly");
const trend = ref<HourlyPoint[]>([]);
const recentPoints = ref<TrafficRawPoint[]>([]);
const loading = ref(true);

function aggregateDaily(points: HourlyPoint[]): HourlyPoint[] {
  const buckets = new Map<number, HourlyPoint>();
  for (const p of points) {
    const dayTs = Math.floor(p.hourTs / 86400) * 86400;
    const existing = buckets.get(dayTs);
    if (existing) {
      existing.inBytes += p.inBytes;
      existing.outBytes += p.outBytes;
      existing.totalBytes += p.totalBytes;
    } else {
      buckets.set(dayTs, { ...p, hourTs: dayTs });
    }
  }
  return Array.from(buckets.values()).sort((a, b) => a.hourTs - b.hourTs);
}

function buildOption(): any {
  const isRecent = granularity.value === "recent";
  const isDaily = granularity.value === "daily";

  if (isRecent) {
    const data = recentPoints.value;
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
          const timeStr = d.toLocaleString("en-US", { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false, timeZone: "UTC" }) + " GMT";
          let html = `<div style="font-weight:700;margin-bottom:6px">${timeStr}</div>`;
          for (const p of params) {
            html += `<div style="display:flex;align-items:center;gap:6px;margin:3px 0">`;
            html += `<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:${p.color}"></span>`;
            html += `<span>${p.seriesName}: <b>${formatBytes(p.value[1])}</b></span></div>`;
          }
          return html;
        },
      },
      legend: {
        data: ["Inbound", "Outbound", "Total"],
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
            return d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", timeZone: "UTC", hour12: false });
          },
        },
      },
      yAxis: {
        type: "value",
        axisLine: { show: false },
        splitLine: { lineStyle: { color: "#f0f4f8" } },
        axisLabel: {
          color: "#7a869a",
          fontSize: 12,
          formatter: (value: number) => formatBytes(value),
        },
      },
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
          labelFormatter(value: number) {
            const d = new Date(value);
            return d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", timeZone: "UTC", hour12: false });
          },
        },
        { type: "inside" },
      ],
      series: [
        {
          name: "Inbound",
          type: "line",
          smooth: 0.3,
          symbol: "none",
          lineStyle: { width: 1.5 },
          areaStyle: { opacity: 0.06 },
          itemStyle: { color: "#2563eb" },
          data: data.map((p) => [p.ts * 1000, p.inBytes]),
        },
        {
          name: "Outbound",
          type: "line",
          smooth: 0.3,
          symbol: "none",
          lineStyle: { width: 1.5 },
          areaStyle: { opacity: 0.06 },
          itemStyle: { color: "#06b6d4" },
          data: data.map((p) => [p.ts * 1000, p.outBytes]),
        },
        {
          name: "Total",
          type: "line",
          smooth: 0.3,
          symbol: "none",
          lineStyle: { width: 1.5 },
          areaStyle: { opacity: 0.06 },
          itemStyle: { color: "#22c55e" },
          data: data.map((p) => [p.ts * 1000, p.totalBytes]),
        },
      ],
    } as any;
  }

  const data = isDaily ? aggregateDaily(trend.value) : trend.value;

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
          html += `<div style="display:flex;align-items:center;gap:6px;margin:3px 0">`;
          html += `<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:${p.color}"></span>`;
          html += `<span>${p.seriesName}: <b>${formatBytes(p.value[1])}</b></span></div>`;
        }
        return html;
      },
    },
    legend: {
      data: ["Inbound", "Outbound", "Total"],
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
    yAxis: {
      type: "value",
      axisLine: { show: false },
      splitLine: { lineStyle: { color: "#f0f4f8" } },
      axisLabel: {
        color: "#7a869a",
        fontSize: 12,
        formatter: (value: number) => formatBytes(value),
      },
    },
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
        labelFormatter(value: number) {
          const d = new Date(value);
          return isDaily
            ? d.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })
            : d.toLocaleTimeString("en-US", { hour: "2-digit", minute: "2-digit", timeZone: "UTC", hour12: false });
        },
      },
      { type: "inside" },
    ],
    series: [
      {
        name: "Inbound",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#2563eb" },
        data: data.map((p) => [p.hourTs * 1000, p.inBytes]),
      },
      {
        name: "Outbound",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#06b6d4" },
        data: data.map((p) => [p.hourTs * 1000, p.outBytes]),
      },
      {
        name: "Total",
        type: "line",
        smooth: 0.3,
        symbol: "circle",
        symbolSize: 5,
        showSymbol: true,
        lineStyle: { width: 2 },
        areaStyle: { opacity: 0.06 },
        itemStyle: { color: "#22c55e" },
        data: data.map((p) => [p.hourTs * 1000, p.totalBytes]),
      },
    ],
  } as any;
}

let resizeHandler: (() => void) | undefined;

onMounted(async () => {
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
  loading.value = false;
  await nextTick();
  if (chartRef.value) {
    chart.value = echarts.init(chartRef.value);
    chart.value.setOption(buildOption());
    resizeHandler = () => chart.value?.resize();
    window.addEventListener("resize", resizeHandler);
  }
});

onUnmounted(() => {
  if (resizeHandler) window.removeEventListener("resize", resizeHandler);
  chart.value?.dispose();
});

watch(granularity, () => {
  chart.value?.setOption(buildOption(), true);
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
          <p class="modal-subtitle">Traffic Trend</p>
        </div>
        <div class="modal-controls">
          <div class="toggle-group">
            <button :class="{ active: granularity === 'recent' }" @click="granularity = 'recent'">Recent</button>
            <button :class="{ active: granularity === 'hourly' }" @click="granularity = 'hourly'">Hourly</button>
            <button :class="{ active: granularity === 'daily' }" @click="granularity = 'daily'">Daily</button>
          </div>
          <button class="close-btn" @click="close" aria-label="Close">&times;</button>
        </div>
      </div>
      <div v-if="loading" class="chart-loading">Loading trend data...</div>
      <div v-show="!loading" ref="chartRef" class="chart-container"></div>
    </div>
  </div>
</template>
