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

export interface SourceSummary {
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
