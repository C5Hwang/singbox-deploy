import { formatBytes } from "./utils";
import { gmtLabel, shiftToTz, tzOffsetMinutes } from "./timezone";
import type { HourlyPoint, ResourceHourlyPoint } from "./types";

export type TimeUnit = "second" | "minute" | "hour" | "day";

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

// A week of minutes is too long an axis for bare clock times — 14:00 turns up
// seven times — and too short for dates alone. The tick that lands on midnight
// carries the date and the rest carry the time, so the axis reads correctly at
// both ends of the zoom the slider covers.
export function fmtDayOrTime(value: number): string {
  const d = shiftToTz(value);
  return d.getUTCHours() === 0 && d.getUTCMinutes() === 0 ? fmtDate(value) : fmtTime(value);
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
        formatter: (value: number) =>
          unit === "day" ? fmtDate(value) : unit === "minute" ? fmtDayOrTime(value) : fmtTime(value),
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
  data: [number, number | null][],
  opts: { yAxisIndex?: number; showSymbol?: boolean; dense?: boolean } = {},
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
    // A week of one-minute rounds is more points than the plot has pixels.
    // Downsampling picks which of them to draw; it does not decide which of
    // them exist, so zooming in still reaches every round, and lttb is the
    // rule that keeps the spikes rather than averaging them away.
    ...(opts.dense ? { sampling: "lttb" } : {}),
    data,
  };
}

// Average lines are horizontal and span the whole plot, so two series with
// similar averages put their labels in the same place. Spreading the labels
// along the line instead of stacking them at one end is what makes collision
// impossible rather than merely unlikely: each series takes a different slot,
// so the labels cannot land on each other however close the values are.
const AVERAGE_LABEL_X = ["insideStart", "insideMiddle", "insideEnd"];

// Which side of its own line a label sits on is not free at the edges of the
// plot: an average close to zero — the common case, since one busy hour lifts
// the maximum far above the mean — has no room underneath, and a label put
// there lands on top of the time axis. So the label sits above its line unless
// the line is high enough that above would leave the plot.
const AVERAGE_HIGH = 0.75;

// Slots are handed out by value proximity rather than by series order: two
// averages far apart do not collide however they are placed, and only the ones
// that are close need to be pulled apart. Three horizontal slots come first
// because separation across the plot reads better than a stack; a fourth close
// average is lifted a chip clear of the first.
const AVERAGE_NEAR_Y = 0.06;

interface AverageSlot {
  height: number;
  slot: number;
}

function averageSlots(series: any[]): (AverageSlot | null)[] {
  const all: number[] = [];
  const means = series.map((s) => {
    const values = seriesValues(s.data);
    all.push(...values);
    return values.length > 0 ? values.reduce((a, b) => a + b, 0) / values.length : null;
  });
  // Zero anchors the range because these axes start there, so a mean near zero
  // is recognised as sitting at the bottom of the plot rather than in the
  // middle of whatever narrow band the data happens to occupy.
  const top = Math.max(0, ...all);
  const span = top - Math.min(0, ...all);
  const slots: (AverageSlot | null)[] = means.map((mean) =>
    mean === null ? null : { height: span > 0 ? (mean - Math.min(0, ...all)) / span : 0, slot: 0 },
  );

  const placed: AverageSlot[] = [];
  for (const entry of slots
    .filter((s): s is AverageSlot => s !== null)
    .slice()
    .sort((a, b) => b.height - a.height)) {
    while (placed.some((p) => p.slot === entry.slot && Math.abs(p.height - entry.height) < AVERAGE_NEAR_Y)) {
      entry.slot++;
    }
    placed.push(entry);
  }
  return slots;
}

// A peak in the last fifth of the series would push its label off the right
// edge, so that one is labelled to the left of the marker instead of above it.
const PEAK_FLIP_FRACTION = 0.8;

// Peak chips are pinned to a data point, so unlike the average labels they
// cannot be spread along the plot — the only free direction is up. Two peaks
// collide when they are close along both axes at once, which is the normal case
// for series that are related by construction: a total peaks where its largest
// component does, within a pixel or two, so both chips land on the same spot.
//
// A chip that has been pushed up by one slot no longer collides with the one
// below it, so the test is run against each chip's placed position rather than
// its marker, and levels are assigned highest peak first — the topmost chip
// keeps the marker and the ones underneath stack above it in value order.
// Thresholds are in normalised plot space: a chip is roughly a fifteenth of the
// plot tall and a seventh of it wide.
const PEAK_NEAR_X = 0.14;
const PEAK_LEVEL_Y = 0.09;

interface PeakAnchor {
  x: number;
  y: number;
  level: number;
}

function peakAnchors(series: any[]): (PeakAnchor | null)[] {
  const raw = series.map((s) => {
    const points = s.data ?? [];
    let bestIndex = -1;
    let best = -Infinity;
    for (let i = 0; i < points.length; i++) {
      const value = Number(Array.isArray(points[i]) ? points[i][1] : points[i]);
      if (Number.isFinite(value) && value > best) {
        best = value;
        bestIndex = i;
      }
    }
    if (bestIndex < 0) return null;
    return { x: points.length < 2 ? 0 : bestIndex / (points.length - 1), value: best };
  });

  // Values only have to be comparable to each other, so they are scaled by the
  // largest peak on the chart. Series on a second axis are laid out against the
  // same scale, which is approximate but errs towards separating chips.
  const top = Math.max(...raw.map((a) => (a ? a.value : -Infinity)));
  const bottom = Math.min(...raw.map((a) => (a ? a.value : Infinity)));
  const span = top - bottom;
  const anchors: (PeakAnchor | null)[] = raw.map((a) =>
    a ? { x: a.x, y: span > 0 ? (a.value - bottom) / span : 0, level: 0 } : null,
  );

  const placed: PeakAnchor[] = [];
  const order = anchors
    .map((a, i) => ({ a, i }))
    .filter((e): e is { a: PeakAnchor; i: number } => e.a !== null)
    .sort((p, q) => q.a.y - p.a.y);
  for (const { a } of order) {
    while (
      placed.some(
        (p) =>
          Math.abs(p.x - a.x) < PEAK_NEAR_X &&
          Math.abs(p.y + p.level * PEAK_LEVEL_Y - (a.y + a.level * PEAK_LEVEL_Y)) < PEAK_LEVEL_Y,
      )
    ) {
      a.level++;
    }
    placed.push(a);
  }
  return anchors;
}

function seriesValues(data: any[]): number[] {
  const values: number[] = [];
  for (const point of data ?? []) {
    const value = Number(Array.isArray(point) ? point[1] : point);
    if (Number.isFinite(value)) values.push(value);
  }
  return values;
}

// withPeakAverage overlays each series with its own average line and peak
// marker. ECharts computes both from the data already on the chart, so the
// overlay can never disagree with the curve it annotates, and nulls — a fully
// lost latency round, a gap in a sparse series — are skipped by both.
//
// Both labels are drawn as filled chips in the series colour with white text.
// A bare coloured label sat directly on the curves and the area fills it was
// annotating and became unreadable wherever they were dense; a chip carries its
// own background, so it reads against the plot instead of competing with it.
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
  // One stacking slot is a chip plus the gap that keeps two of them from
  // touching, which is what the normalised PEAK_LEVEL_Y stands in for.
  const levelHeight = narrow ? 17 : 21;
  const chip = (color: string) => ({
    color: "#ffffff",
    backgroundColor: color,
    padding: narrow ? [2, 4] : [3, 6],
    borderRadius: 5,
    fontSize,
    fontWeight: 700,
  });
  const anchors = peakAnchors(series);
  const averages = averageSlots(series);
  return series.map((s, i) => {
    const color = s.itemStyle?.color ?? "#2563eb";
    const values = seriesValues(s.data);
    const anchor = anchors[i];
    const average = averages[i];
    const slot = average?.slot ?? 0;
    // Above the line unless the line is too high for above; the stack then
    // grows in whichever direction the label already faces.
    const above = (average?.height ?? 0) < AVERAGE_HIGH;
    const averagePosition = `${AVERAGE_LABEL_X[slot % AVERAGE_LABEL_X.length]}${above ? "Top" : "Bottom"}`;
    const averageOffset = Math.floor(slot / AVERAGE_LABEL_X.length) * levelHeight;
    // A series with nothing in it has no average and no peak to draw; emitting
    // them anyway would put a chip on the zero line of an empty chart.
    const hasData = show && values.length > 0;
    return {
      ...s,
      markLine: {
        silent: true,
        symbol: "none",
        animation: true,
        animationDuration: 320,
        animationEasing: "cubicOut",
        // Recessive next to the curves: this is an annotation of the data, not
        // another reading of it.
        lineStyle: { type: "dashed", width: 1, color, opacity: 0.55 },
        label: {
          ...chip(color),
          position: averagePosition,
          distance: 4,
          offset: [0, above ? -averageOffset : averageOffset],
          formatter: ({ value }: any) => `avg ${format(Number(value))}`,
        },
        emphasis: { disabled: true },
        data: hasData ? [{ type: "average" }] : [],
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
          ...chip(color),
          position: (anchor?.x ?? 0) > PEAK_FLIP_FRACTION ? "left" : "top",
          distance: 7,
          // Negative y is up: a chip that would land on one already placed is
          // lifted clear of it rather than drawn over it.
          offset: [0, -(anchor?.level ?? 0) * levelHeight],
          formatter: ({ value }: any) => `peak ${format(Number(value))}`,
        },
        emphasis: { disabled: true },
        data: hasData ? [{ type: "max" }] : [],
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
