import { formatBytes } from "./utils";

export type TimeUnit = "second" | "hour" | "day";

const MONTH_DAY: Intl.DateTimeFormatOptions = { month: "short", day: "numeric", timeZone: "UTC" };
const HOUR_MIN: Intl.DateTimeFormatOptions = { hour: "2-digit", minute: "2-digit", hour12: false, timeZone: "UTC" };

export function fmtDate(value: number): string {
  return new Date(value).toLocaleDateString("en-US", MONTH_DAY);
}

export function fmtTime(value: number): string {
  return new Date(value).toLocaleTimeString("en-US", HOUR_MIN);
}

export function fmtTooltipTime(value: number, unit: TimeUnit): string {
  const d = new Date(value);
  if (unit === "day") {
    return d.toLocaleDateString("en-US", { ...MONTH_DAY, year: "numeric" });
  }
  if (unit === "second") {
    return d.toLocaleString("en-US", { ...MONTH_DAY, ...HOUR_MIN, second: "2-digit" }) + " GMT";
  }
  return d.toLocaleString("en-US", { ...MONTH_DAY, ...HOUR_MIN }) + " GMT";
}

function tooltipFormatter(unit: TimeUnit, valueText: (p: any) => string) {
  return (params: any) => {
    if (!Array.isArray(params) || params.length === 0) return "";
    let html = `<div style="font-weight:700;margin-bottom:6px">${fmtTooltipTime(params[0].value[0], unit)}</div>`;
    for (const p of params) {
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
}

export interface ChartFrame {
  narrow: boolean;
  option: Record<string, any>;
}

// Shared chart skeleton: tooltip, legend, grid, time axis and zoom slider,
// sized for the available width so axes and legend never collide on phones.
export function buildFrame({ width, unit, legend, tooltipUnit, tooltipValue }: FrameParams): ChartFrame {
  const narrow = width < 600;
  // Slider handle labels render outside the track; keep enough inset on both
  // sides so the two-line "date / time" label stays inside the canvas.
  const sliderInset = narrow ? 56 : 76;
  const legendRows = narrow && legend.length > 3 ? 2 : 1;
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
      formatter: tooltipFormatter(tooltipUnit, tooltipValue),
    },
    legend: {
      data: legend,
      top: 0,
      left: "center",
      itemGap: narrow ? 10 : 20,
      itemWidth: narrow ? 16 : 25,
      textStyle: { fontSize: narrow ? 11 : 13, fontWeight: 600 },
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
