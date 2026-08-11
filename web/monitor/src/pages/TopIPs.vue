<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import IPTrendModal from "../components/IPTrendModal.vue";
import { fetchIPTraffic } from "../api";
import { locations, resolveLocations } from "../geo";
import { formatBytes, formatDateTime } from "../utils";
import type { IPDirectionKey, IPTrafficRow, IPTrafficWindow, IPWindowKey, IPSort, Summary } from "../types";

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
// Ranking defaults to the window the quota is counted over, largest first,
// which is the question the page exists to answer.
const sort = ref<IPSort>({ window: "cycle", direction: "totalBytes", descending: true });
const rows = ref<IPTrafficRow[]>([]);
const cycleStart = ref(0);
const unavailableNodes = ref<string[]>([]);
const disabledNodes = ref<string[]>([]);
const loading = ref(false);
const loadError = ref("");
const modalRow = ref<IPTrafficRow | null>(null);

// Every window carries all three directions, so the header is a matrix and any
// cell in it can order the table.
const windows: { key: IPWindowKey; label: string }[] = [
  { key: "today", label: "Today" },
  { key: "last7", label: "Last 7 days" },
  { key: "cycle", label: "This cycle" },
];
const directions: { key: IPDirectionKey; label: string }[] = [
  { key: "inBytes", label: "In" },
  { key: "outBytes", label: "Out" },
  { key: "totalBytes", label: "Total" },
];

function emptyWindow(): IPTrafficWindow {
  return { inBytes: 0, outBytes: 0, totalBytes: 0 };
}

function addWindow(into: IPTrafficWindow, from: IPTrafficWindow) {
  into.inBytes += from.inBytes;
  into.outBytes += from.outBytes;
  into.totalBytes += from.totalBytes;
}

// Nodes are queried in parallel and merged by address: the same client reaching
// two nodes is one row whose totals are the sum, which is what a "top 30" over
// a fleet has to mean.
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
        cycle: emptyWindow(),
        today: emptyWindow(),
        last7: emptyWindow(),
      };
      row.nodes.push(source.name ?? sourceKey(source));
      addWindow(row.cycle, entry.cycle);
      addWindow(row.today, entry.today);
      addWindow(row.last7, entry.last7);
      merged.set(entry.ip, row);
    }
  }

  rows.value = [...merged.values()];
  cycleStart.value = latestCycle;
  unavailableNodes.value = unavailable;
  disabledNodes.value = disabled;
  loadError.value = unavailable.length === targets.length && targets.length > 0 ? "no node returned per-IP data" : "";
  loading.value = false;
  resolveLocations(visible.value.map((r) => r.ip));
}

// Clicking a cell sorts by it; clicking the same cell again reverses it. A new
// cell always starts descending, because "who used the most" is the question
// nine times out of ten.
function sortBy(window: IPWindowKey, direction: IPDirectionKey) {
  if (sort.value.window === window && sort.value.direction === direction) {
    sort.value = { ...sort.value, descending: !sort.value.descending };
    return;
  }
  sort.value = { window, direction, descending: true };
}

function isSorted(window: IPWindowKey, direction: IPDirectionKey): boolean {
  return sort.value.window === window && sort.value.direction === direction;
}

const sorted = computed(() => {
  const { window, direction, descending } = sort.value;
  const value = (row: IPTrafficRow) => row[window][direction];
  return [...rows.value].sort((a, b) => (descending ? value(b) - value(a) : value(a) - value(b)));
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

// The modal drills into whichever nodes the table is currently showing, so the
// chart it opens describes the same numbers the row does.
const modalSources = computed(() =>
  selected.value === ALL_NODES ? sources.value.map(sourceKey) : [selected.value],
);
</script>

<template>
  <section class="grid">
    <article class="card span-12 topips-head">
      <div>
        <p class="eyebrow">Top talkers</p>
        <p class="metric-value small">Busiest {{ SHOWN_ROWS }} client addresses</p>
        <p class="metric-detail">
          Counted per remote address for connections opened to the node. Click a column to sort by it, or a row for its trend.
          <span v-if="cycleLabel"> Cleared with the traffic quota, last reset {{ cycleLabel }}.</span>
        </p>
      </div>
      <label class="picker">
        <span class="eyebrow">Node</span>
        <select v-model="selected">
          <option :value="ALL_NODES">All nodes</option>
          <option v-for="source in sources" :key="sourceKey(source)" :value="sourceKey(source)">{{ source.name }}</option>
        </select>
      </label>
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
            <tr class="window-row">
              <th class="rank" rowspan="2">#</th>
              <th rowspan="2">Address</th>
              <th rowspan="2">Location</th>
              <th v-if="selected === ALL_NODES" rowspan="2">Nodes</th>
              <th v-for="w in windows" :key="w.key" colspan="3" class="window-head">{{ w.label }}</th>
            </tr>
            <tr>
              <template v-for="w in windows" :key="w.key">
                <th
                  v-for="d in directions"
                  :key="`${w.key}-${d.key}`"
                  class="num sortable"
                  :class="{ sorted: isSorted(w.key, d.key), first: d.key === 'inBytes' }"
                  :aria-sort="isSorted(w.key, d.key) ? (sort.descending ? 'descending' : 'ascending') : 'none'"
                  @click="sortBy(w.key, d.key)"
                >
                  {{ d.label }}
                  <span class="caret" :class="{ up: isSorted(w.key, d.key) && !sort.descending }">{{ isSorted(w.key, d.key) ? "▾" : "" }}</span>
                </th>
              </template>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in visible" :key="row.ip" class="ip-row" @click="modalRow = row">
              <td class="rank">{{ i + 1 }}</td>
              <td class="address">{{ row.ip }}</td>
              <td class="location">{{ locations[row.ip] || "…" }}</td>
              <td v-if="selected === ALL_NODES" class="nodes">{{ row.nodes.join(", ") }}</td>
              <template v-for="w in windows" :key="w.key">
                <td
                  v-for="d in directions"
                  :key="`${w.key}-${d.key}`"
                  class="num"
                  :class="{ sorted: isSorted(w.key, d.key), strong: d.key === 'totalBytes', first: d.key === 'inBytes' }"
                >
                  {{ formatBytes(row[w.key][d.key]) }}
                </td>
              </template>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="geo-note">Locations are resolved by your browser, not by the node.</p>
    </article>
  </section>

  <IPTrendModal
    v-if="modalRow"
    :row="modalRow"
    :location="locations[modalRow.ip] || ''"
    :sources="modalSources"
    @close="modalRow = null"
  />
</template>

<style scoped>
.topips-head { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 16px; }
.picker { display: flex; flex-direction: column; gap: 7px; }
.picker select {
  border: 1px solid var(--line); border-radius: 12px; padding: 9px 12px;
  background: white; color: var(--text); font: inherit; font-size: 14px; font-weight: 650;
  min-width: 180px; cursor: pointer;
}
.table-scroll { overflow-x: auto; }
.ip-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.ip-table th {
  padding: 0 10px 9px; color: var(--muted); font-size: 11px; font-weight: 750;
  letter-spacing: 0.04em; text-transform: uppercase; text-align: left; white-space: nowrap;
}
.window-row th.window-head {
  text-align: center; padding-bottom: 6px; color: var(--text);
  border-bottom: 1px solid var(--line);
}
.ip-table th.num, .ip-table td.num { text-align: right; }
.ip-table th.first, .ip-table td.first { border-left: 1px solid var(--line); }
.sortable { cursor: pointer; user-select: none; transition: color 0.15s; }
.sortable:hover { color: var(--blue); }
.sortable.sorted { color: var(--blue); }
.caret { display: inline-block; width: 9px; font-size: 10px; }
.caret.up { transform: rotate(180deg); }
.ip-table td { padding: 11px 10px; border-top: 1px solid var(--line); white-space: nowrap; }
.ip-row { cursor: pointer; transition: background 0.15s; }
.ip-row:hover { background: #f6f9fd; }
.rank { width: 34px; color: var(--muted); font-weight: 750; font-variant-numeric: tabular-nums; }
.address { font-weight: 750; font-variant-numeric: tabular-nums; }
.location, .nodes { color: var(--muted); font-weight: 600; white-space: normal; min-width: 120px; }
.num { font-variant-numeric: tabular-nums; }
.num.strong { font-weight: 800; }
td.num.sorted { color: var(--blue); }
.geo-note {
  margin: 16px 0 0; padding-top: 14px; border-top: 1px solid var(--line);
  color: var(--muted); font-size: 12px; font-weight: 600;
}
</style>
