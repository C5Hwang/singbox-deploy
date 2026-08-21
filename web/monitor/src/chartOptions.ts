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

// Any axis finer than a day runs into the same thing: a span of several days is
// too long for bare clock times — 14:00 turns up once a day — and too short for
// dates alone. The tick that lands on midnight carries the date and the rest
// carry the time, so the axis reads correctly at both ends of the zoom the
// slider covers, whether its slots are minutes, hours or raw samples.
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
  // Height of the chart element. Only the annotation layout needs it, to know
  // how far a pixel of offset moves a chip through the value range.
  height?: number;
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
  plotHeight: number;
  option: Record<string, any>;
}

// Shared chart skeleton: tooltip, legend, grid, time axis and zoom slider,
// sized for the available width so axes and legend never collide on phones.
// Reached only through buildTrendOption, so a chart cannot take the frame and
// then quietly disagree with the rest of the dashboard about the other half.
function buildFrame({ width, height, unit, legend, tooltipUnit, tooltipValue, sortTooltip }: FrameParams): ChartFrame {
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
        // One rule for every chart: a daily axis is dates all the way along, and
        // anything finer is times with the date on the tick that crosses into a
        // new day. Without that tick the same week reads as a single unlabelled
        // day.
        formatter: (value: number) => (unit === "day" ? fmtDate(value) : fmtDayOrTime(value)),
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
  // What is left of the element once the legend, the axis and the zoom slider
  // have taken their bands.
  const plotHeight = Math.max(120, (height ?? 0) - option.grid.top - option.grid.bottom);
  return { narrow, plotHeight, option };
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

// Chip placement.
//
// Both annotations put a filled chip on the plot: the average line's rides its
// own line and can slide anywhere along it, the peak's is pinned to one data
// point and can only move away from it. Two chips overlapping is what makes the
// overlay unreadable, and the two kinds overlap each other as readily as they
// overlap their own kind — nine latency probes that all spike at once put nine
// peak chips and nine average chips into the same corner. So they are placed in
// one pass against one list of what is already on the plot.
//
// Positions are normalised: x from the left edge of the plot to the right, y
// from the bottom of the value range to the top. A chip is about a fifteenth of
// the plot tall and a seventh of it wide, which is what the two thresholds say.
const CHIP_NEAR_X = 0.14;

// How tall one chip plus its breathing room is, in pixels. The vertical part of
// the model is measured in these rather than in normalised space, because the
// offset that moves a chip is in pixels: a band that reserved more room than
// the offset actually travels would report chips as separated while they were
// still on top of each other.
const CHIP_PX = 21;
const CHIP_PX_NARROW = 17;

// Height of the plot area, used to convert those pixels into the normalised
// space the values live in. Callers pass the real one; this is what a modal
// chart comes to when nobody says.
const DEFAULT_PLOT_PX = 380;

// Where each of the average label's three slots sits along the plot. They are
// tried in order, so an average that has the plot to itself takes the left one
// and only crowded ones travel.
const AVERAGE_LABEL_X = ["insideStart", "insideMiddle", "insideEnd"];
const AVERAGE_SLOT_X = [0.12, 0.5, 0.88];

// An average close to zero — the common case, since one busy hour lifts the
// maximum far above the mean — has no room underneath, and a label put there
// lands on the time axis. So a label sits above its line unless the line is
// high enough that above would leave the plot.
const AVERAGE_HIGH = 0.75;

// A peak in the last fifth of the series would push its label off the right
// edge, so that one is labelled to the left of the marker instead of above it.
const PEAK_FLIP_FRACTION = 0.8;

// Levels are tried alternating above and below rather than climbing, so a chip
// that has to move ends up as near its own mark as the crowd allows instead of
// at the top of a ladder whose rungs no longer point at anything.
// Capped deliberately. A chip three slots from its own mark is at the limit of
// still pointing at it; past that the honest failure is a little overlap in one
// crowded corner rather than a column of chips that have left their marks
// behind. Charts with a handful of series never reach the cap.
const LEVEL_ORDER = [0, 1, -1, 2, -2, 3, -3];

// How close to the edge of the plot a chip may be pushed. Without this the
// levels below a peak that is already near the axis walk the chip off the grid
// and onto the time labels, which is worse than the overlap they were avoiding.
const PLOT_MARGIN = 0.04;

interface Placed {
  x: number;
  y: number;
}

// place finds the nearest free level for a chip whose mark is at (x, y), adds it
// to taken, and reports the level. Level is in chip heights, positive upwards.
// Levels that would leave the plot are not offered, so a crowd at the bottom
// stacks upwards rather than off the bottom edge.
function place(taken: Placed[], step: number, x: number, y: number, order: number[] = LEVEL_ORDER): number {
  const allowed = order.filter((l) => {
    const at = y + l * step;
    return at >= PLOT_MARGIN && at <= 1 - PLOT_MARGIN;
  });
  for (const level of allowed.length > 0 ? allowed : [0]) {
    const at = y + level * step;
    if (!taken.some((p) => Math.abs(p.x - x) < CHIP_NEAR_X && Math.abs(p.y - at) < step)) {
      taken.push({ x, y: at });
      return level;
    }
  }
  const last = allowed.length > 0 ? allowed[allowed.length - 1] : 0;
  taken.push({ x, y: y + last * step });
  return last;
}

interface PeakPlacement {
  x: number;
  level: number;
}

interface AveragePlacement {
  slot: number;
  above: boolean;
  level: number;
}

interface Placements {
  peaks: (PeakPlacement | null)[];
  averages: (AveragePlacement | null)[];
}

// layOutChips computes every chip's position on one shared plot. Peaks go first
// because they are pinned — an average can always find another slot along its
// line, so it is the one that should give way.
function layOutChips(series: any[], step: number): Placements {
  const all: number[] = [];
  const marks = series.map((s) => {
    const points = s.data ?? [];
    let bestIndex = -1;
    let best = -Infinity;
    let sum = 0;
    let count = 0;
    for (let i = 0; i < points.length; i++) {
      const value = pointValue(points[i]);
      if (value === null) continue;
      all.push(value);
      sum += value;
      count++;
      if (value > best) {
        best = value;
        bestIndex = i;
      }
    }
    if (count === 0) return null;
    return {
      peakX: points.length < 2 ? 0 : bestIndex / (points.length - 1),
      peak: best,
      mean: sum / count,
    };
  });

  // Zero anchors the range because these axes start there, so a mean near zero
  // is recognised as sitting at the bottom of the plot rather than in the middle
  // of whatever narrow band the data happens to occupy. Peaks and averages share
  // the scale, which is what lets one list hold both.
  const bottom = Math.min(0, ...all);
  const span = Math.max(0, ...all) - bottom;
  const height = (v: number) => (span > 0 ? (v - bottom) / span : 0);

  const taken: Placed[] = [];
  const peaks: (PeakPlacement | null)[] = series.map(() => null);
  const averages: (AveragePlacement | null)[] = series.map(() => null);

  // Largest first. Placement is greedy, so whoever goes first keeps its true
  // position and everyone after works around it — and the mark worth reading
  // exactly is the highest one, not the ninth-highest.
  const byPeak = marks
    .map((m, i) => ({ m, i }))
    .filter((e) => e.m !== null)
    .sort((a, b) => b.m!.peak - a.m!.peak);
  for (const { m, i } of byPeak) {
    peaks[i] = { x: m!.peakX, level: place(taken, step, m!.peakX, height(m!.peak)) };
  }

  const byMean = marks
    .map((m, i) => ({ m, i }))
    .filter((e) => e.m !== null)
    .sort((a, b) => b.m!.mean - a.m!.mean);
  for (const { m, i } of byMean) {
    const y = height(m!.mean);
    const above = y < AVERAGE_HIGH;
    // Every slot is costed on a copy, then the cheapest one is taken for real:
    // a slot that needs no lift beats one that needs three, whatever order the
    // slots are listed in.
    let best = { slot: 0, level: Infinity };
    for (let slot = 0; slot < AVERAGE_SLOT_X.length; slot++) {
      const level = place(taken.slice(), step, AVERAGE_SLOT_X[slot], y, above ? LEVEL_ORDER : LEVEL_ORDER.map((l) => -l));
      if (Math.abs(level) < Math.abs(best.level)) best = { slot, level };
      if (level === 0) break;
    }
    place(taken, step, AVERAGE_SLOT_X[best.slot], y, [best.level]);
    // Signed, because the render multiplies it by the same positive-is-up
    // convention: an unsigned level would draw the chip on the opposite side of
    // the line from where the layout placed it.
    averages[i] = { slot: best.slot, above, level: best.level };
  }
  return { peaks, averages };
}

// Chips are laid out one y-axis at a time. A dual-axis chart draws percentages
// against one axis and byte rates against the other, and the two have no shared
// scale: measured together, a rate of a few megabytes a second would put every
// percentage on the floor of the plot and report them all as colliding.
function layOutByAxis(series: any[], step: number): Placements {
  const peaks: (PeakPlacement | null)[] = series.map(() => null);
  const averages: (AveragePlacement | null)[] = series.map(() => null);
  const byAxis = new Map<number, number[]>();
  series.forEach((s, i) => {
    const axis = s.yAxisIndex ?? 0;
    const group = byAxis.get(axis);
    if (group) group.push(i);
    else byAxis.set(axis, [i]);
  });
  for (const indices of byAxis.values()) {
    const placed = layOutChips(indices.map((i) => series[i]), step);
    indices.forEach((index, k) => {
      peaks[index] = placed.peaks[k];
      averages[index] = placed.averages[k];
    });
  }
  return { peaks, averages };
}

// A gap in a series is null, and Number(null) is 0 — a finite number, and a
// wrong one. Reading a point has to reject the gap before the coercion, or a
// week of one-minute rounds with nine tenths of its slots empty reports an
// average a tenth of the real one.
function pointValue(point: any): number | null {
  const raw = Array.isArray(point) ? point[1] : point;
  if (raw === null || raw === undefined) return null;
  const value = Number(raw);
  return Number.isFinite(value) ? value : null;
}

function seriesValues(data: any[]): number[] {
  const values: number[] = [];
  for (const point of data ?? []) {
    const value = pointValue(point);
    if (value !== null) values.push(value);
  }
  return values;
}

export type PeakAverageFormat = (value: number, series: any) => string;

// withPeakAverage overlays each series with its own average line and peak
// marker. ECharts computes both from the data already on the chart, so the
// overlay can never disagree with the curve it annotates, and nulls — a fully
// lost latency round, a gap in a sparse series — are skipped by both.
//
// Both labels are drawn as filled chips in the series colour with white text. A
// chip carries its own background, so it reads against a dense plot instead of
// competing with the curves and area fills it sits on.
//
// The marks are always emitted, empty when hidden: the toggle updates the chart
// by merging rather than rebuilding it, and a merge only removes what it is
// handed. That is also what keeps the transition to the curve itself: the lines
// stay where they are while the annotations fade in over them.
//
// format is given the series as well as the value, because a chart whose lines
// are not all in the same unit — percentages against one axis, byte rates
// against the other — has to label each chip in the unit its own line is drawn
// in.
function withPeakAverage(
  series: any[],
  {
    show,
    format,
    narrow,
    plotHeight,
  }: { show: boolean; format: PeakAverageFormat; narrow: boolean; plotHeight?: number },
): any[] {
  const fontSize = narrow ? 10 : 11;
  const levelHeight = narrow ? CHIP_PX_NARROW : CHIP_PX;
  const step = levelHeight / Math.max(120, plotHeight ?? DEFAULT_PLOT_PX);
  const chip = (color: string) => ({
    color: "#ffffff",
    backgroundColor: color,
    padding: narrow ? [2, 4] : [3, 6],
    borderRadius: 5,
    fontSize,
    fontWeight: 700,
  });
  const { peaks, averages } = layOutByAxis(series, step);
  return series.map((s, i) => {
    const color = s.itemStyle?.color ?? "#2563eb";
    const values = seriesValues(s.data);
    const peak = peaks[i];
    const average = averages[i];
    const above = average?.above ?? true;
    const averagePosition = `${AVERAGE_LABEL_X[average?.slot ?? 0]}${above ? "Top" : "Bottom"}`;
    // Negative y is up and a level is positive upwards, the same convention the
    // peak chips use.
    const averageOffset = -(average?.level ?? 0) * levelHeight;
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
          offset: [0, averageOffset],
          formatter: ({ value }: any) => `avg ${format(Number(value), s)}`,
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
          position: (peak?.x ?? 0) > PEAK_FLIP_FRACTION ? "left" : "top",
          distance: 7,
          // Negative y is up, and a level is positive upwards, so a chip that
          // would land on one already placed moves clear of it.
          offset: [0, -(peak?.level ?? 0) * levelHeight],
          formatter: ({ value }: any) => `peak ${format(Number(value), s)}`,
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

export function msAxis(narrow: boolean) {
  return {
    type: "value",
    name: narrow ? "" : "ms",
    // A latency axis that started at the lowest reading would turn the noise
    // between 30 and 32 ms into a mountain range.
    min: 0,
    axisLine: { show: false },
    splitLine: { lineStyle: { color: "#f0f4f8" } },
    axisLabel: { color: "#7a869a", fontSize: narrow ? 10 : 12, formatter: (v: number) => `${v}` },
  };
}

// Whatever the bucket, a traffic point is a timestamp and three byte counts,
// so the raw samples, the hourly buckets and one address's history are all the
// same shape here once the caller has said which field carries the timestamp.
export interface TrafficPoint {
  ts: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

const TRAFFIC_LINES: { name: string; color: string; key: "inBytes" | "outBytes" | "totalBytes" }[] = [
  { name: "Inbound", color: "#2563eb", key: "inBytes" },
  { name: "Outbound", color: "#06b6d4", key: "outBytes" },
  { name: "Total", color: "#22c55e", key: "totalBytes" },
];

export const TRAFFIC_LEGEND = TRAFFIC_LINES.map((line) => line.name);

// The same three lines in the same three colours wherever traffic is charted —
// a node's own modal and one address's — so a reader who has learnt the colours
// on one page has learnt them on the other.
export function trafficSeries(points: TrafficPoint[], showSymbol: boolean): any[] {
  return TRAFFIC_LINES.map((line) =>
    lineSeries(line.name, line.color, points.map((p) => [p.ts * 1000, p[line.key]]), { showSymbol }),
  );
}

export interface TrendParams extends Omit<FrameParams, "width" | "height"> {
  // The chart's own element, which is what the frame is sized against. It does
  // not exist until the modal has rendered once, and a desktop width is assumed
  // until it does.
  el?: HTMLElement | null;
  // Both are given the frame's own answer to whether the chart is narrow, so a
  // series can drop its point symbols and an axis its name on the same terms
  // the frame drops legend rows.
  yAxis: (narrow: boolean) => any;
  series: (narrow: boolean) => any[];
  peakAverage: { show: boolean; format: PeakAverageFormat };
}

// The whole option for one trend chart: the shared frame, the value axis and
// the annotated series. Every trend modal in the dashboard is built through
// here and differs only in what it hands over, which is what makes them one
// chart shown five times rather than five charts that merely resemble each
// other.
export function buildTrendOption({ el, yAxis, series, peakAverage, ...frame }: TrendParams): Record<string, any> {
  const { narrow, plotHeight, option } = buildFrame({
    ...frame,
    width: el?.clientWidth ?? 800,
    height: el?.clientHeight ?? 0,
  });
  return {
    ...option,
    yAxis: yAxis(narrow),
    series: withPeakAverage(series(narrow), { ...peakAverage, narrow, plotHeight }),
  };
}
