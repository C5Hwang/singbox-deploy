import type { TimeUnit } from "./chartOptions";

// The granularities a trend chart offers. Every chart that offers any offers
// them from this one list, so "Recent" means the same window and "Daily" the
// same bucket wherever the reader meets them.
export type TrendMode = "recent" | "hourly" | "daily" | "hourly-avg" | "hourly-max" | "daily-avg" | "daily-max";

export interface ModeOption {
  key: TrendMode;
  label: string;
}

// Traffic is a total: an hour's bytes are an hour's bytes, and there is nothing
// to average or take the maximum of.
export const TRAFFIC_MODES: ModeOption[] = [
  { key: "recent", label: "Recent" },
  { key: "hourly", label: "Hourly" },
  { key: "daily", label: "Daily" },
];

// A resource reading is a level rather than a total, so a bucket of them has
// both an average and a peak and the chart has to say which one it is drawing.
export const RESOURCE_MODES: ModeOption[] = [
  { key: "recent", label: "Recent" },
  { key: "hourly-avg", label: "Hourly (Avg)" },
  { key: "hourly-max", label: "Hourly (Max)" },
  { key: "daily-avg", label: "Daily (Avg)" },
  { key: "daily-max", label: "Daily (Max)" },
];

export interface ModeShape {
  // Which table the points come from: the raw samples, or the hourly buckets
  // that daily rolls up from.
  isRecent: boolean;
  isDaily: boolean;
  // Which aggregate a bucket is read at. Meaningless for traffic, whose modes
  // never carry a suffix, and false there.
  isMax: boolean;
  unit: TimeUnit;
  tooltipUnit: TimeUnit;
}

// One reading of a mode for every chart. Each modal used to derive this for
// itself, and they drifted: the same word picked a different axis unit or a
// different tooltip depending on which chart it was clicked on.
export function modeShape(mode: TrendMode): ModeShape {
  const isRecent = mode === "recent";
  const isDaily = mode.startsWith("daily");
  const unit: TimeUnit = isDaily ? "day" : "hour";
  return {
    isRecent,
    isDaily,
    isMax: mode.endsWith("max"),
    unit,
    // Raw samples are seconds apart, so their tooltip says which second; a
    // bucket's says which bucket.
    tooltipUnit: isRecent ? "second" : unit,
  };
}
