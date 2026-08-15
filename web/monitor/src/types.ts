export interface HourlyPoint {
  hourTs: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

export interface ResourceSnapshot {
  cpuPct: number;
  memPct: number;
  memUsedBytes: number;
  memTotalBytes: number;
  diskUsagePct: number;
  diskUsedBytes: number;
  diskTotalBytes: number;
  diskIOReadRate: number;
  diskIOWriteRate: number;
}

export interface TrafficRawPoint {
  ts: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

export interface ResourceRawPoint {
  ts: number;
  cpuPct: number;
  memPct: number;
  diskPct: number;
  dioRead: number;
  dioWrite: number;
}

export interface ResourceHourlyPoint {
  hourTs: number;
  cpuAvg: number;
  cpuMax: number;
  memAvg: number;
  memMax: number;
  diskAvg: number;
  diskMax: number;
  dioReadAvg: number;
  dioReadMax: number;
  dioWriteAvg: number;
  dioWriteMax: number;
}

// The dashboard's top-level pages, in sidebar order.
export type Tab = "traffic" | "resources" | "topips" | "latency" | "relay";

// One bucket of an address's history, at whichever granularity it was read at.
export interface IPSeriesPoint {
  ts: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

// One address's traffic over one of the windows the table ranks by.
export interface IPTrafficWindow {
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

export interface IPTrafficEntry {
  ip: string;
  cycle: IPTrafficWindow;
  today: IPTrafficWindow;
  last7: IPTrafficWindow;
}

export interface IPTrafficSnapshot {
  enabled: boolean;
  cycleStart: number;
  entries: IPTrafficEntry[];
}

export interface IPTrafficDetail {
  ip: string;
  recent: IPSeriesPoint[];
  hourly: IPSeriesPoint[];
  daily: IPSeriesPoint[];
}

// A merged row: one address's traffic summed across the nodes it reached.
export interface IPTrafficRow {
  ip: string;
  nodes: string[];
  cycle: IPTrafficWindow;
  today: IPTrafficWindow;
  last7: IPTrafficWindow;
}

// Every cell in the table is sortable, so a sort is a window plus a direction
// within it rather than one of a few named orders.
export type IPWindowKey = "today" | "last7" | "cycle";
export type IPDirectionKey = "inBytes" | "outBytes" | "totalBytes";
export interface IPSort {
  window: IPWindowKey;
  direction: IPDirectionKey;
  descending: boolean;
}

// RELAY_TARGET_KIND marks the probes a relay runs against the landing nodes it
// fronts, so they get their own page instead of appearing as carriers with no
// carrier and no city.
export const RELAY_TARGET_KIND = "relay";

export interface PingTarget {
  id: string;
  // carrier and city name a probe from the fixed list. A relay probe has
  // neither and carries name instead.
  carrier?: string;
  city?: string;
  address: string;
  kind?: string;
  name?: string;
}

export function isRelayTarget(target: PingTarget): boolean {
  return target.kind === RELAY_TARGET_KIND;
}

export function carrierTargets(targets: PingTarget[]): PingTarget[] {
  return targets.filter((t) => !t.kind);
}

export function relayTargets(targets: PingTarget[]): PingTarget[] {
  return targets.filter(isRelayTarget);
}

// avgMs is null when every probe was lost, which draws as a gap rather than as
// zero latency.
export interface PingLatestPoint {
  target: string;
  ts: number;
  avgMs: number | null;
  lossPct: number;
}

// What the latency page polls: the probe list and the newest round each. The
// history is a separate fetch because it is a week of one-minute rounds and it
// only changes by one slot a minute.
export interface LatencySnapshot {
  targets: PingTarget[];
  latest: PingLatestPoint[];
}

// One target's week on a fixed grid: slot i is the round at start + i*step.
// ms is null where a round answered nothing, loss is -1 where no round ran.
export interface PingTrack {
  ms: (number | null)[];
  loss: number[];
}

export interface PingSeries {
  start: number;
  step: number;
  count: number;
  series: Record<string, PingTrack>;
}

export interface SourceSummary {
  id: string;
  name: string;
  fetchedAt?: string;
  sampledAt?: string;
  monitorURL?: string;
  inUsedBytes: number;
  outUsedBytes: number;
  totalUsedBytes: number;
  inRemainingBytes: number;
  outRemainingBytes: number;
  totalRemainingBytes: number;
  inLimitBytes: number;
  outLimitBytes: number;
  totalLimitBytes: number;
  resetTime: string;
  resources?: ResourceSnapshot;
}

export interface Summary {
  inUsedBytes: number;
  outUsedBytes: number;
  totalUsedBytes: number;
  inRemainingBytes: number;
  outRemainingBytes: number;
  totalRemainingBytes: number;
  inLimitBytes: number;
  outLimitBytes: number;
  totalLimitBytes: number;
  resetTime: string;
  resources?: ResourceSnapshot;
  sources?: SourceSummary[];
}

// Overview-card metric opened in the all-sources trend modal.
export type MetricDef =
  | { kind: "traffic"; title: string; key: "inBytes" | "outBytes" | "totalBytes" }
  | { kind: "resource"; title: string; key: "cpu" | "mem" | "disk" };

export interface UsageRow {
  label: string;
  key: "in" | "out" | "total";
  used: number;
  limit: number;
  color: string;
}
