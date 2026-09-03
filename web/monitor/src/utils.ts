import { gmtLabel, shiftToTz, tzOffsetMinutes } from "./timezone";

export function formatBytes(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(Number(value))) return "NA";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  let size = Math.max(0, Number(value));
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  const digits = index === 0 ? 0 : size >= 10 ? 1 : 2;
  return `${size.toFixed(digits)} ${units[index]}`;
}

// formatBytesCompact is the table variant: nine numeric columns on one row do
// not have space for "190.6 MB", and at a glance the unit letter carries the
// magnitude just as well as the word does.
export function formatBytesCompact(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(Number(value))) return "—";
  let size = Math.max(0, Number(value));
  if (size === 0) return "0";
  const units = ["", "K", "M", "G", "T", "P"];
  let index = 0;
  while (size >= 1024 && index < units.length - 1) {
    size /= 1024;
    index += 1;
  }
  const digits = size >= 100 || index === 0 ? 0 : size >= 10 ? 0 : 1;
  return `${size.toFixed(digits)}${units[index]}`;
}

export function formatRate(bytesPerSec: number | null | undefined): string {
  if (bytesPerSec === null || bytesPerSec === undefined) return "NA";
  return `${formatBytes(bytesPerSec)}/s`;
}

export function percentFor(used: number, limit: number): number | null {
  if (limit <= 0) return null;
  return Math.min(100, Math.max(0, (used / limit) * 100));
}

export function percentText(value: number | null): string {
  if (value === null) return "Unlimited";
  return `${Math.round(value)}%`;
}

export function tone(percent: number | null): string {
  if (percent !== null && percent >= 90) return " danger";
  if (percent !== null && percent >= 75) return " warn";
  return "";
}

export function barStyle(percent: number | null, color: string): Record<string, string> {
  return {
    "--value": String(percent === null ? 0 : percent),
    "--bar": color,
  };
}

export function formatDateTime(value: string | number | Date): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "NA";
  const text = shiftToTz(date).toLocaleString("en-US", { hour12: false, timeZone: "UTC" });
  return `${text} ${gmtLabel(tzOffsetMinutes.value)}`;
}

// A node's headline usage: the fullest of whichever quotas it has, or null when
// it has none. It decides the ring on the traffic card, the running/limited
// pill, and the fleet's online count in the sidebar, so it is computed once
// rather than three ways that agree today.
export function peakPercent(source: {
  inUsedBytes: number; inLimitBytes: number;
  outUsedBytes: number; outLimitBytes: number;
  totalUsedBytes: number; totalLimitBytes: number;
}): number | null {
  const configured = [
    percentFor(source.inUsedBytes, source.inLimitBytes),
    percentFor(source.outUsedBytes, source.outLimitBytes),
    percentFor(source.totalUsedBytes, source.totalLimitBytes),
  ].filter((p): p is number => p !== null);
  if (configured.length === 0) return null;
  return Math.max(...configured);
}

export function isLimited(source: Parameters<typeof peakPercent>[0]): boolean {
  const peak = peakPercent(source);
  return peak !== null && peak >= 100;
}
