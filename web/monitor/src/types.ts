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
  // Traffic the node forwarded to a landing node as a relay, kept apart from
  // what the same address did against the node directly.
  relayed?: boolean;
  // Which landing node it was forwarded to, and what that node is called. Both
  // are absent on a direct entry, and on a relayed one a node recorded before
  // it told landing nodes apart.
  landing?: string;
  landingName?: string;
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

// One strand of an address's traffic: what it did against the fleet directly,
// or what the fleet relayed for it to one landing node. It is the unit the
// nodes actually report and the unit a chart can be drawn for, which is why the
// row above is a sum of these rather than a reading of its own.
export interface IPTrafficSegment {
  relayed: boolean;
  // landing is the destination's registry ID, empty on a direct segment and on
  // a relayed one whose node predates per-landing accounting.
  landing: string;
  // label is what the breakdown shows: "Direct", the landing node's name, or a
  // stand-in for a destination this fleet can no longer name.
  label: string;
  nodes: string[];
  cycle: IPTrafficWindow;
  today: IPTrafficWindow;
  last7: IPTrafficWindow;
}

// A merged row: one address's whole traffic, summed across the nodes it reached
// and across everything those nodes carried for it. Relayed strands stay
// readable one by one in segments, so the row answers "how much" and the
// breakdown answers "to where".
export interface IPTrafficRow {
  ip: string;
  relayed: boolean;
  nodes: string[];
  segments: IPTrafficSegment[];
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

// The probe ID a relay measures one landing node under. It mirrors the ID the
// relay mints for that probe, which is what pairs a link the hub reports with
// the reading that belongs to it.
export function relayTargetID(landing: string): string {
  return `${RELAY_TARGET_KIND}:${landing.trim().toLowerCase()}`;
}

// One fronting relationship, as the hub's registry has it: which relay carries
// which landing node, and whether it is forwarding right now. A relay only
// probes what it forwards, so this is the only thing that keeps a stood-down
// landing node on the relay page instead of dropping it without a word.
export interface RelayLink {
  // relay is the dashboard source ID of the node that forwards.
  relay: string;
  // landing is the fronted node's registry ID.
  landing: string;
  name?: string;
  forwarding: boolean;
  // reason says why a link is not forwarding — most often that one end has run
  // out of traffic for the cycle.
  reason?: string;
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
  // The limits are what applies right now: the configured limit plus this
  // cycle's traffic package. The package fields say how much of each limit is
  // the package, so the card can draw the two apart; a node built before
  // packages existed leaves them out.
  inLimitBytes: number;
  outLimitBytes: number;
  totalLimitBytes: number;
  inPackageBytes?: number;
  outPackageBytes?: number;
  totalPackageBytes?: number;
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
  inPackageBytes?: number;
  outPackageBytes?: number;
  totalPackageBytes?: number;
  resetTime: string;
  resources?: ResourceSnapshot;
  sources?: SourceSummary[];
  // How many nodes are fronted by a relay. Zero means the fleet has no relay
  // topology at all, and the dashboard drops the relay page rather than
  // offering one that can only ever say "nothing here".
  relayLinks?: number;
  // Which sources do the relaying. The relay page asks these and only these for
  // their probes, so a node that relays for nobody — including one that is down
  // — cannot slow down a page it has nothing to do with.
  relayNodes?: string[];
  // Every link the hub's registry holds, standing or stood down. The relay page
  // draws a row per link rather than per probe, which is what keeps a landing
  // node on screen while the hub is not forwarding to it. A spoke's own
  // dashboard has no registry to send and leaves this out.
  relayTopology?: RelayLink[];
}

// Overview-card metric opened in the all-sources trend modal.
export type MetricDef =
  | { kind: "traffic"; title: string; key: "inBytes" | "outBytes" | "totalBytes" }
  | { kind: "resource"; title: string; key: "cpu" | "mem" | "disk" };

export interface UsageRow {
  label: string;
  key: "in" | "out" | "total";
  used: number;
  // limit is the allowance in force; pkg is the part of it that is a traffic
  // package granted for this cycle rather than the configured limit.
  limit: number;
  pkg: number;
  color: string;
}
