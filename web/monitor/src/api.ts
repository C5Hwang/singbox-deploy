import type { Summary, HourlyPoint, ResourceHourlyPoint, TrafficRawPoint, ResourceRawPoint } from "./types";

export async function fetchSummary(): Promise<Summary> {
  const res = await fetch("api/summary", { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export async function fetchTrafficTrend(source?: string): Promise<HourlyPoint[]> {
  const params = source ? `?source=${encodeURIComponent(source)}` : "";
  const res = await fetch(`api/traffic-trend${params}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  return data.trend ?? [];
}

export async function fetchTrafficRecent(source?: string): Promise<TrafficRawPoint[]> {
  const params = source ? `?source=${encodeURIComponent(source)}` : "";
  const res = await fetch(`api/traffic-recent${params}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  return data.points ?? [];
}

export async function fetchResourceTrend(source?: string): Promise<ResourceHourlyPoint[]> {
  const params = source ? `?source=${encodeURIComponent(source)}` : "";
  const res = await fetch(`api/resource-trend${params}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  return data.trend ?? [];
}

export async function fetchResourceRecent(source?: string): Promise<ResourceRawPoint[]> {
  const params = source ? `?source=${encodeURIComponent(source)}` : "";
  const res = await fetch(`api/resource-recent${params}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const data = await res.json();
  return data.points ?? [];
}
