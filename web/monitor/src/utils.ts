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

export function formatGMTDateTime(value: string | number | Date): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "NA";
  return date.toLocaleString("en-US", {
    hour12: false,
    timeZone: "UTC",
    timeZoneName: "short",
  }).replace(/\bUTC\b/g, "GMT");
}
