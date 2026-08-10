<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import IPTrendModal from "../components/IPTrendModal.vue";
import { fetchIPTraffic } from "../api";
import { DEFAULT_GEO_ENDPOINT, geoEndpoint, locations, resolveLocations, setGeoEndpoint } from "../geo";
import { formatBytes, formatDateTime } from "../utils";
import type { IPDailyPoint, IPSortKey, IPTrafficRow, Summary } from "../types";

// The dashboard shows thirty; each node returns more so that merging several
// nodes' lists produces the true top thirty rather than the top of each.
const SHOWN_ROWS = 30;
const ALL_NODES = "__all__";

const props = defineProps<{ summary: Summary | null }>();

const sources = computed(() => {
  const list = props.summary?.sources ?? [];
  if (list.length > 0) return list;
  return props.summary ? [{ id: "local", name: "Local Server" }] : [];
});

function sourceKey(source: { id?: string; name?: string }): string {
  return source.id || source.name || "";
}

const selected = ref<string>(ALL_NODES);
const sortKey = ref<IPSortKey>("total");
const rows = ref<IPTrafficRow[]>([]);
const cycleStart = ref(0);
const unavailableNodes = ref<string[]>([]);
const disabledNodes = ref<string[]>([]);
const loading = ref(false);
const loadError = ref("");
const modalRow = ref<IPTrafficRow | null>(null);
const endpointDraft = ref(geoEndpoint.value);

const sortOptions: { key: IPSortKey; label: string }[] = [
  { key: "total", label: "This cycle" },
  { key: "today", label: "Today" },
  { key: "last7", label: "Last 7 days" },
];

// Day buckets are GMT, matching the GMT quota reset the rest of the dashboard
// is keyed to, so the windows are computed in GMT rather than in the viewer's
// display timezone.
function gmtDayStart(now = Date.now()): number {
  return Math.floor(now / 1000 / 86400) * 86400;
}

function windowTotal(daily: IPDailyPoint[], days: number): number {
  const from = gmtDayStart() - (days - 1) * 86400;
  return daily.reduce((sum, p) => (p.dayTs >= from ? sum + p.totalBytes : sum), 0);
}

// Nodes are queried in parallel and merged by address: the same client reaching
// two nodes is one row whose total is the sum, which is what a "top 30" over a
// fleet has to mean.
async function load() {
  const targets = selected.value === ALL_NODES ? sources.value : sources.value.filter((s) => sourceKey(s) === selected.value);
  if (targets.length === 0) return;
  loading.value = true;
  const merged = new Map<string, IPTrafficRow>();
  const unavailable: string[] = [];
  const disabled: string[] = [];
  let latestCycle = 0;

  const results = await Promise.all(
    targets.map(async (source) => {
      try {
        return { source, snapshot: await fetchIPTraffic(sourceKey(source)) };
      } catch {
        return { source, snapshot: null };
      }
    }),
  );

  for (const { source, snapshot } of results) {
    if (!snapshot) {
      unavailable.push(source.name ?? sourceKey(source));
      continue;
    }
    if (!snapshot.enabled) disabled.push(source.name ?? sourceKey(source));
    latestCycle = Math.max(latestCycle, snapshot.cycleStart);
    for (const entry of snapshot.entries) {
      const row = merged.get(entry.ip) ?? {
        ip: entry.ip,
        nodes: [],
        inBytes: 0,
        outBytes: 0,
        totalBytes: 0,
        todayBytes: 0,
        last7Bytes: 0,
        daily: [],
      };
      row.nodes.push(source.name ?? sourceKey(source));
      row.inBytes += entry.inBytes;
      row.outBytes += entry.outBytes;
      row.totalBytes += entry.totalBytes;
      row.daily = mergeDaily(row.daily, entry.daily);
      merged.set(entry.ip, row);
    }
  }

  const list = [...merged.values()];
  for (const row of list) {
    row.todayBytes = windowTotal(row.daily, 1);
    row.last7Bytes = windowTotal(row.daily, 7);
  }
  rows.value = list;
  cycleStart.value = latestCycle;
  unavailableNodes.value = unavailable;
  disabledNodes.value = disabled;
  loadError.value = unavailable.length === targets.length && targets.length > 0 ? "no node returned per-IP data" : "";
  loading.value = false;
  resolveLocations(visible.value.map((r) => r.ip));
}

function mergeDaily(into: IPDailyPoint[], from: IPDailyPoint[]): IPDailyPoint[] {
  const byDay = new Map(into.map((p) => [p.dayTs, { ...p }]));
  for (const point of from) {
    const existing = byDay.get(point.dayTs);
    if (existing) {
      existing.inBytes += point.inBytes;
      existing.outBytes += point.outBytes;
      existing.totalBytes += point.totalBytes;
    } else {
      byDay.set(point.dayTs, { ...point });
    }
  }
  return [...byDay.values()].sort((a, b) => a.dayTs - b.dayTs);
}

const sorted = computed(() => {
  const key: keyof IPTrafficRow = sortKey.value === "total" ? "totalBytes" : sortKey.value === "today" ? "todayBytes" : "last7Bytes";
  return [...rows.value].sort((a, b) => (b[key] as number) - (a[key] as number));
});

const visible = computed(() => sorted.value.slice(0, SHOWN_ROWS));

watch(visible, (list) => resolveLocations(list.map((r) => r.ip)));
watch([selected, () => sources.value.length], load, { immediate: true });

// Counters advance once per sampling interval, so a slow refresh keeps the
// table current without hammering every node in the fleet.
let refreshTimer: number | undefined;
onMounted(() => {
  refreshTimer = window.setInterval(load, 60000);
});
onUnmounted(() => {
  if (refreshTimer) window.clearInterval(refreshTimer);
});

const cycleLabel = computed(() => (cycleStart.value > 0 ? formatDateTime(cycleStart.value * 1000) : ""));

function applyEndpoint() {
  setGeoEndpoint(endpointDraft.value);
  endpointDraft.value = geoEndpoint.value;
  resolveLocations(visible.value.map((r) => r.ip));
}
</script>

<template>
  <section class="grid">
    <article class="card span-12 topips-head">
      <div>
        <p class="eyebrow">Top talkers</p>
        <p class="metric-value small">Busiest {{ SHOWN_ROWS }} client addresses</p>
        <p class="metric-detail">
          Counted per remote address for connections opened to the node.
          <span v-if="cycleLabel"> Cleared with the traffic quota, last reset {{ cycleLabel }}.</span>
        </p>
      </div>
      <div class="controls">
        <label class="picker">
          <span class="eyebrow">Node</span>
          <select v-model="selected">
            <option :value="ALL_NODES">All nodes</option>
            <option v-for="source in sources" :key="sourceKey(source)" :value="sourceKey(source)">{{ source.name }}</option>
          </select>
        </label>
        <div class="picker">
          <span class="eyebrow">Sort by</span>
          <div class="toggle-group">
            <button v-for="option in sortOptions" :key="option.key" :class="{ active: sortKey === option.key }" @click="sortKey = option.key">
              {{ option.label }}
            </button>
          </div>
        </div>
      </div>
    </article>
  </section>

  <p v-if="disabledNodes.length" class="no-data">
    Per-IP accounting is unavailable on {{ disabledNodes.join(", ") }}: the host has no nftables utility.
  </p>
  <p v-if="unavailableNodes.length" class="no-data">
    No per-IP data from {{ unavailableNodes.join(", ") }}. A node still running an older agent does not report it
    until it is upgraded.
  </p>

  <section class="grid sources">
    <article class="card span-12">
      <p v-if="loading && rows.length === 0" class="no-data">Loading per-IP traffic...</p>
      <p v-else-if="loadError" class="no-data">Per-IP traffic is unavailable: {{ loadError }}.</p>
      <p v-else-if="visible.length === 0" class="no-data">
        No client traffic recorded in this quota cycle yet.
      </p>
      <div v-else class="table-scroll">
        <table class="ip-table">
          <thead>
            <tr>
              <th class="rank">#</th>
              <th>Address</th>
              <th>Location</th>
              <th v-if="selected === ALL_NODES">Nodes</th>
              <th class="num">Inbound</th>
              <th class="num">Outbound</th>
              <th class="num">Today</th>
              <th class="num">7 days</th>
              <th class="num">Cycle</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in visible" :key="row.ip" class="ip-row" @click="modalRow = row">
              <td class="rank">{{ i + 1 }}</td>
              <td class="address">{{ row.ip }}</td>
              <td class="location">{{ locations[row.ip] || "…" }}</td>
              <td v-if="selected === ALL_NODES" class="nodes">{{ row.nodes.join(", ") }}</td>
              <td class="num">{{ formatBytes(row.inBytes) }}</td>
              <td class="num">{{ formatBytes(row.outBytes) }}</td>
              <td class="num">{{ formatBytes(row.todayBytes) }}</td>
              <td class="num">{{ formatBytes(row.last7Bytes) }}</td>
              <td class="num strong">{{ formatBytes(row.totalBytes) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="geo-note">
        Locations are resolved by your browser, not by the node.
        <label>
          Lookup URL
          <input v-model="endpointDraft" spellcheck="false" :placeholder="DEFAULT_GEO_ENDPOINT" @keyup.enter="applyEndpoint" />
        </label>
        <button @click="applyEndpoint">Apply</button>
      </p>
    </article>
  </section>

  <IPTrendModal v-if="modalRow" :row="modalRow" :location="locations[modalRow.ip] || ''" @close="modalRow = null" />
</template>

<style scoped>
.topips-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 16px; }
.controls { display: flex; flex-wrap: wrap; align-items: flex-end; gap: 14px; }
.picker { display: flex; flex-direction: column; gap: 7px; }
.picker select {
  border: 1px solid var(--line); border-radius: 12px; padding: 9px 12px;
  background: white; color: var(--text); font: inherit; font-size: 14px; font-weight: 650;
  min-width: 180px; cursor: pointer;
}
.table-scroll { overflow-x: auto; }
.ip-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.ip-table th {
  padding: 0 12px 10px; color: var(--muted); font-size: 11px; font-weight: 750;
  letter-spacing: 0.04em; text-transform: uppercase; text-align: left; white-space: nowrap;
}
.ip-table td { padding: 11px 12px; border-top: 1px solid var(--line); white-space: nowrap; }
.ip-row { cursor: pointer; transition: background 0.15s; }
.ip-row:hover { background: #f6f9fd; }
.rank { width: 34px; color: var(--muted); font-weight: 750; font-variant-numeric: tabular-nums; }
.address { font-weight: 750; font-variant-numeric: tabular-nums; }
.location, .nodes { color: var(--muted); font-weight: 600; white-space: normal; min-width: 130px; }
.num { text-align: right; font-variant-numeric: tabular-nums; }
.num.strong { font-weight: 800; }
.geo-note {
  display: flex; flex-wrap: wrap; align-items: center; gap: 10px;
  margin: 16px 0 0; padding-top: 14px; border-top: 1px solid var(--line);
  color: var(--muted); font-size: 12px; font-weight: 600;
}
.geo-note label { display: flex; align-items: center; gap: 8px; }
.geo-note input {
  border: 1px solid var(--line); border-radius: 10px; padding: 7px 10px;
  background: white; color: var(--text); font: inherit; font-size: 12px; min-width: 240px;
}
.geo-note button {
  border: 1px solid var(--line); border-radius: 10px; padding: 7px 14px;
  background: white; color: var(--text); font: inherit; font-size: 12px; font-weight: 700; cursor: pointer;
}
.geo-note button:hover { background: #f0f4f8; }
</style>
