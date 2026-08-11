import { formatBytes } from "./utils";
import { gmtLabel, shiftToTz, tzOffsetMinutes } from "./timezone";
import type { HourlyPoint, ResourceHourlyPoint } from "./types";

export type TimeUnit = "second" | "hour" | "day";

// Machine-identity colors for multi-source charts, assigned by source order.
// Ordered so adjacent hues stay distinguishable under color-vision deficiency.
export const SOURCE_COLORS = [
  "#2563eb", "#f59e0b", "#06b6d4", "#ec4899", "#22c55e",
  "#8b5cf6", "#f97316", "#0d9488", "#d946ef", "#65a30d",
];

// Days are bucketed at midnight in the selected display timezone so daily
// values line up with the dates shown on the axis.
function tzDayStart(hourTs: number): number {
  const offsetSec = tzOffsetMinutes.value * 60;
  return Math.floor((hourTs + offsetSec) / 86400) * 86400 - offsetSec;
}

export function aggregateTrafficDaily(points: HourlyPoint[]): HourlyPoint[] {
  const buckets = new Map<number, HourlyPoint>();
  for (const p of points) {
    const dayTs = tzDayStart(p.hourTs);
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

function avg(arr: number[]): number {
  if (arr.length === 0) return 0;
  return arr.reduce((a, b) => a + b, 0) / arr.length;
}

function agg(arr: number[], isMax: boolean): number {
  if (arr.length === 0) return 0;
  return isMax ? Math.max(...arr) : avg(arr);
}

export function aggregateResourceDaily(points: ResourceHourlyPoint[], isMax: boolean): ResourceHourlyPoint[] {
  const buckets = new Map<number, ResourceHourlyPoint[]>();
  for (const p of points) {
    const dayTs = tzDayStart(p.hourTs);
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

// Timestamps are pre-shifted into the selected display offset, so the "UTC"
// here only stops the browser from shifting them a second time.
const MONTH_DAY: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", timeZone: "UTC" };
const HOUR_MIN: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" };

export function fmtDate(value: number): string {
  return shiftToTz(value).toLocaleDateString("en-US", MONTH_DAY);
}

export function fmtTime(value: number): string {
  return shiftToTz(value).toLocaleTimeString("en-US", HOUR_MIN);
}

export function fmtTooltipTime(value: number, unit: TimeUnit): string {
  const d = shiftToTz(value);
  if (unit === "day") {
    return d.toLocaleDateString("en-US", { ...MONTH_DAY, year: "numeric" });
  }
  const label = gmtLabel(tzOffsetMinutes.value);
  if (unit === "second") {
    return d.toLocaleString("en-US", { ...MONTH_DAY, ...HOUR_MIN, second: "2-digit" }) + ` ${label}`;
  }
  return d.toLocaleString("en-US", { ...MONTH_DAY, ...HOUR_MIN }) + ` ${label}`;
}

function tooltipValueOf(p: any): number {
  const value = Number(Array.isArray(p.value) ? p.value[1] : p.value);
  return Number.isFinite(value) ? value : -Infinity;
}

function tooltipFormatter(unit: TimeUnit, valueText: (p: any) => string, sortByValue: boolean) {
  return (params: any) => {
    if (!Array.isArray(params) || params.length === 0) return "";
    const rows = sortByValue ? [...params].sort((a, b) => tooltipValueOf(b) - tooltipValueOf(a)) : params;
    let html = `<div style="font-weight:700;margin-bottom:6px">${fmtTooltipTime(params[0].value[0], unit)}</div>`;
    for (const p of rows) {
      html +=
        `<div style="display:flex;align-items:center;gap:6px;margin:3px 0">` +
        `<span style="display:inline-block;width:10px;height:10px;border-radius:50%;background:${p.color}"></span>` +
        `<span>${p.seriesName}: <b>${valueText(p)}</b></span></div>`;
    }
    return html;
  };
}

export interface FrameParams {
  width: number;
  unit: TimeUnit;
  legend: string[];
  tooltipUnit: TimeUnit;
  tooltipValue: (p: any) => string;
  // Sort tooltip rows by value (largest first) instead of series order; used
  // by the all-sources chart where machine ranking matters more than order.
  sortTooltip?: boolean;
}

export interface ChartFrame {
  narrow: boolean;
  option: Record<string, any>;
}

// Shared chart skeleton: tooltip, legend, grid, time axis and zoom slider,
// sized for the available width so axes and legend never collide on phones.
export function buildFrame({ width, unit, legend, tooltipUnit, tooltipValue, sortTooltip }: FrameParams): ChartFrame {
  const narrow = width < 600;
  // Slider handle labels render outside the track; keep enough inset on both
  // sides so the two-line "date / time" label stays inside the canvas.
  const sliderInset = narrow ? 56 : 76;
  // Long legends (one entry per machine) scroll in a single row instead of
  // wrapping, so they can never spill into the plot area.
  const scrollLegend = legend.length > 4;
  const legendRows = narrow && !scrollLegend && legend.length > 3 ? 2 : 1;
  const option = {
    animation: true,
    animationDuration: 800,
    animationEasing: "cubicInOut",
    tooltip: {
      trigger: "axis",
      confine: true,
      backgroundColor: "rgba(255,255,255,0.96)",
      borderColor: "#e7ecf4",
      textStyle: { color: "#172033", fontSize: narrow ? 12 : 13 },
      formatter: tooltipFormatter(tooltipUnit, tooltipValue, sortTooltip ?? false),
    },
    legend: {
      data: legend,
      top: 0,
      left: "center",
      itemGap: narrow ? 10 : 20,
      itemWidth: narrow ? 16 : 25,
      textStyle: { fontSize: narrow ? 11 : 13, fontWeight: 600 },
      ...(scrollLegend
        ? {
            type: "scroll",
            pageIconColor: "#526075",
            pageIconInactiveColor: "#c9d4e5",
            pageIconSize: 11,
            pageTextStyle: { color: "#7a869a", fontSize: narrow ? 10 : 11 },
          }
        : {}),
    },
    grid: {
      left: narrow ? 6 : 14,
      right: narrow ? 6 : 14,
      top: narrow ? 22 + legendRows * 22 : 46,
      bottom: narrow ? 44 : 56,
      containLabel: true,
    },
    xAxis: {
      type: "time",
      axisLine: { lineStyle: { color: "#e7ecf4" } },
      axisLabel: {
        color: "#7a869a",
        fontSize: narrow ? 10 : 12,
        hideOverlap: true,
        formatter: (value: number) => (unit === "day" ? fmtDate(value) : fmtTime(value)),
      },
    },
    dataZoom: [
      {
        type: "slider",
        show: true,
        left: sliderInset,
        right: sliderInset,
        bottom: narrow ? 6 : 10,
        height: narrow ? 22 : 28,
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
        textStyle: { fontSize: narrow ? 10 : 11, color: "#7a869a", lineHeight: 14 },
        labelFormatter: (value: number) =>
          unit === "day" ? fmtDate(value) : `${fmtDate(value)}\n${fmtTime(value)}`,
      },
      { type: "inside" },
    ],
  };
  return { narrow, option };
}

export function lineSeries(
  name: string,
  color: string,
  data: [number, number][],
  opts: { yAxisIndex?: number; showSymbol?: boolean } = {},
) {
  return {
    name,
    type: "line",
    smooth: 0.3,
    symbol: opts.showSymbol ? "circle" : "none",
    symbolSize: 5,
    showSymbol: !!opts.showSymbol,
    yAxisIndex: opts.yAxisIndex ?? 0,
    lineStyle: { width: opts.showSymbol ? 2 : 1.5 },
    areaStyle: { opacity: 0.06 },
    itemStyle: { color },
    data,
  };
}

// withPeakAverage overlays each series with its own average line and peak
// marker. ECharts computes both from the data already on the chart, so the
// overlay can never disagree with the curve it annotates, and nulls — a fully
// lost latency round, a gap in a sparse series — are skipped by both.
//
// The marks are always emitted, empty when hidden: the toggle updates the chart
// by merging rather than rebuilding it, and a merge only removes what it is
// handed. That is also what keeps the transition to the curve itself: the lines
// stay where they are while the annotations fade in over them.
export function withPeakAverage(
  series: any[],
  { show, format, narrow }: { show: boolean; format: (v: number) => string; narrow: boolean },
): any[] {
  const fontSize = narrow ? 10 : 11;
  return series.map((s) => {
    const color = s.itemStyle?.color ?? "#2563eb";
    return {
      ...s,
      markLine: {
        silent: true,
        symbol: "none",
        animation: true,
        animationDuration: 320,
        animationEasing: "cubicOut",
        lineStyle: { type: "dashed", width: 1.5, color, opacity: 0.85 },
        label: {
          position: "insideEndTop",
          distance: 3,
          color,
          fontSize,
          fontWeight: 700,
          formatter: ({ value }: any) => `avg ${format(Number(value))}`,
        },
        emphasis: { disabled: true },
        data: show ? [{ type: "average" }] : [],
      },
      markPoint: {
        silent: true,
        symbol: "circle",
        symbolSize: narrow ? 7 : 9,
        animation: true,
        animationDuration: 320,
        animationEasing: "backOut",
        itemStyle: { color, borderColor: "#ffffff", borderWidth: 2 },
        label: {
          position: "top",
          distance: 6,
          color,
          fontSize,
          fontWeight: 700,
          formatter: ({ value }: any) => `peak ${format(Number(value))}`,
        },
        emphasis: { disabled: true },
        data: show ? [{ type: "max" }] : [],
      },
    };
  });
}

export function bytesAxis(narrow: boolean) {
  return {
    type: "value",
    axisLine: { show: false },
    splitLine: { lineStyle: { color: "#f0f4f8" } },
    axisLabel: { color: "#7a869a", fontSize: narrow ? 10 : 12, formatter: (v: number) => formatBytes(v) },
  };
}

export function percentAxis(narrow: boolean) {
  return {
    type: "value",
    name: narrow ? "" : "%",
    min: 0,
    max: 100,
    position: "left",
    axisLine: { show: false },
    splitLine: { lineStyle: { color: "#f0f4f8" } },
    axisLabel: { color: "#7a869a", fontSize: narrow ? 10 : 12, formatter: (v: number) => `${v}%` },
  };
}

export function rateAxis(narrow: boolean) {
  return {
    type: "value",
    name: narrow ? "" : "IO",
    position: "right",
    axisLine: { show: false },
    splitLine: { show: false },
    axisLabel: { color: "#7a869a", fontSize: narrow ? 10 : 12, formatter: (v: number) => `${formatBytes(v)}/s` },
  };
}
