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
export type Tab = "traffic" | "resources" | "topips" | "latency";

export interface IPDailyPoint {
  dayTs: number;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
}

export interface IPTrafficEntry {
  ip: string;
  inBytes: number;
  outBytes: number;
  totalBytes: number;
  daily: IPDailyPoint[];
}

export interface IPTrafficSnapshot {
  enabled: boolean;
  cycleStart: number;
  entries: IPTrafficEntry[];
}

// A merged row: one address's traffic across the nodes it reached, with the
// windows the table sorts by derived from the daily series.
export interface IPTrafficRow {
  ip: string;
  nodes: string[];
  inBytes: number;
  outBytes: number;
  totalBytes: number;
  todayBytes: number;
  last7Bytes: number;
  daily: IPDailyPoint[];
}

export type IPSortKey = "total" | "today" | "last7";

export interface PingTarget {
  id: string;
  carrier: string;
  city: string;
  address: string;
}

// avgMs is null when every probe was lost, which draws as a gap rather than as
// zero latency.
export interface PingLatestPoint {
  target: string;
  ts: number;
  avgMs: number | null;
  lossPct: number;
}

export interface PingHourlyPoint {
  hourTs: number;
  target: string;
  avgMs: number | null;
  lossPct: number;
}

export interface LatencySnapshot {
  targets: PingTarget[];
  latest: PingLatestPoint[];
  points: PingHourlyPoint[];
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
