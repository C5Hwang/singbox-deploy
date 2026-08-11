<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import IPTrendModal from "../components/IPTrendModal.vue";
import { fetchIPTraffic } from "../api";
import { flagFor, locations, resolveLocations } from "../geo";
import { formatBytesCompact, formatDateTime } from "../utils";
import type { IPDirectionKey, IPTrafficRow, IPTrafficWindow, IPWindowKey, IPSort, Summary } from "../types";

// Every address a node still has is listed; thirty of them fit on a page.
const PAGE_SIZE = 30;
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

// Nine numbers per row is a lot of header. The window names carry the words and
// the directions are reduced to arrows, so the second header row is three
// glyphs rather than three more words per group.
const windows: { key: IPWindowKey; label: string }[] = [
  { key: "today", label: "Today" },
  { key: "last7", label: "7 days" },
  { key: "cycle", label: "Cycle" },
];
const directions: { key: IPDirectionKey; glyph: string; title: string }[] = [
  { key: "inBytes", glyph: "↓", title: "Inbound" },
  { key: "outBytes", glyph: "↑", title: "Outbound" },
  { key: "totalBytes", glyph: "Σ", title: "Total" },
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

const pageCount = computed(() => Math.max(1, Math.ceil(sorted.value.length / PAGE_SIZE)));
const page = ref(1);

// Re-sorting or switching node re-ranks the whole list, so the page the reader
// was on no longer means anything; a shorter list can also strand them past the
// end. Both cases go back to the first page.
watch([sort, selected], () => (page.value = 1));
watch(pageCount, (count) => {
  if (page.value > count) page.value = count;
});

const visible = computed(() => sorted.value.slice((page.value - 1) * PAGE_SIZE, page.value * PAGE_SIZE));
// The rank column counts through the whole ranking, not through the page.
const firstRank = computed(() => (page.value - 1) * PAGE_SIZE + 1);

// Enough page buttons to jump around without a strip that wraps: the ends are
// always reachable and the current position always has neighbours.
const pageButtons = computed<(number | "gap")[]>(() => {
  const total = pageCount.value;
  const current = page.value;
  if (total <= 7) return Array.from({ length: total }, (_, i) => i + 1);
  const pages = new Set([1, total, current, current - 1, current + 1]);
  if (current <= 3) [2, 3, 4].forEach((p) => pages.add(p));
  if (current >= total - 2) [total - 3, total - 2, total - 1].forEach((p) => pages.add(p));
  const ordered = [...pages].filter((p) => p >= 1 && p <= total).sort((a, b) => a - b);
  const out: (number | "gap")[] = [];
  for (let i = 0; i < ordered.length; i++) {
    if (i > 0 && ordered[i] - ordered[i - 1] > 1) out.push("gap");
    out.push(ordered[i]);
  }
  return out;
});

// A share bar behind the leading column turns the ranking into something the
// eye reads before the numbers do. It is scaled to the whole ranking rather
// than to the page, so page two does not re-inflate its own rows to full width.
const topValue = computed(() => {
  const { window, direction } = sort.value;
  return Math.max(1, ...sorted.value.map((r) => r[window][direction]));
});

function shareStyle(row: IPTrafficRow): Record<string, string> {
  const { window, direction } = sort.value;
  return { "--share": `${Math.max(0, Math.min(100, (row[window][direction] / topValue.value) * 100))}%` };
}

function placeOf(ip: string) {
  return locations.value[ip] ?? { country: "", code: "", city: "" };
}

// The modal has one line for the whole place, so its two columns join back up.
function placeLabel(ip: string): string {
  const place = placeOf(ip);
  return [place.country, place.city].filter(Boolean).join(" · ");
}

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
        <p class="metric-value small">{{ rows.length }} client address{{ rows.length === 1 ? "" : "es" }}</p>
        <p v-if="cycleLabel" class="metric-detail">Cycle from {{ cycleLabel }}</p>
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
    No per-IP data from {{ unavailableNodes.join(", ") }}.
  </p>

  <section class="grid sources">
    <article class="card span-12 table-card">
      <p v-if="loading && rows.length === 0" class="no-data">Loading per-IP traffic...</p>
      <p v-else-if="loadError" class="no-data">Per-IP traffic is unavailable: {{ loadError }}.</p>
      <p v-else-if="visible.length === 0" class="no-data">
        No client traffic recorded in this quota cycle yet.
      </p>
      <div v-else class="table-scroll">
        <table class="ip-table">
          <thead>
            <tr class="band">
              <th class="rank"></th>
              <th></th>
              <th></th>
              <th></th>
              <th v-if="selected === ALL_NODES"></th>
              <th v-for="w in windows" :key="w.key" colspan="3" class="band-label">
                <span>{{ w.label }}</span>
              </th>
            </tr>
            <tr class="heads">
              <th class="rank">#</th>
              <th class="col-address">Address</th>
              <th class="col-country">Country</th>
              <th class="col-place">City</th>
              <th v-if="selected === ALL_NODES" class="col-nodes">Nodes</th>
              <template v-for="w in windows" :key="w.key">
                <th
                  v-for="d in directions"
                  :key="`${w.key}-${d.key}`"
                  class="num sortable"
                  :class="{ sorted: isSorted(w.key, d.key), lead: d.key === 'inBytes' }"
                  :title="`${w.label} · ${d.title}`"
                  :aria-sort="isSorted(w.key, d.key) ? (sort.descending ? 'descending' : 'ascending') : 'none'"
                  @click="sortBy(w.key, d.key)"
                >
                  <span class="glyph">{{ d.glyph }}</span>
                  <span class="caret" :class="{ up: isSorted(w.key, d.key) && !sort.descending }">{{ isSorted(w.key, d.key) ? "▾" : "" }}</span>
                </th>
              </template>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, i) in visible" :key="row.ip" class="ip-row" @click="modalRow = row">
              <td class="rank">{{ firstRank + i }}</td>
              <td class="address" :style="shareStyle(row)"><span>{{ row.ip }}</span></td>
              <td class="country" :title="placeOf(row.ip).country">
                <span v-if="placeOf(row.ip).code" class="flag">{{ flagFor(placeOf(row.ip).code) }}</span>
                <span class="country-name">{{ placeOf(row.ip).country || "—" }}</span>
              </td>
              <td class="place">{{ placeOf(row.ip).city || "—" }}</td>
              <td v-if="selected === ALL_NODES" class="nodes">
                <span v-for="node in row.nodes" :key="node" class="node-chip">{{ node }}</span>
              </td>
              <template v-for="w in windows" :key="w.key">
                <td
                  v-for="d in directions"
                  :key="`${w.key}-${d.key}`"
                  class="num"
                  :class="{ sorted: isSorted(w.key, d.key), strong: d.key === 'totalBytes', lead: d.key === 'inBytes' }"
                >
                  {{ formatBytesCompact(row[w.key][d.key]) }}
                </td>
              </template>
            </tr>
          </tbody>
        </table>
      </div>

      <nav v-if="pageCount > 1" class="pager" aria-label="Pagination">
        <button class="page-step" :disabled="page === 1" aria-label="Previous page" @click="page = page - 1">‹</button>
        <template v-for="(entry, i) in pageButtons" :key="`${entry}-${i}`">
          <span v-if="entry === 'gap'" class="page-gap">…</span>
          <button
            v-else
            class="page-num"
            :class="{ on: entry === page }"
            :aria-current="entry === page ? 'page' : undefined"
            @click="page = entry as number"
          >
            {{ entry }}
          </button>
        </template>
        <button class="page-step" :disabled="page === pageCount" aria-label="Next page" @click="page = page + 1">›</button>
      </nav>
    </article>
  </section>

  <IPTrendModal
    v-if="modalRow"
    :row="modalRow"
    :location="placeLabel(modalRow.ip)"
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
.table-card { padding: 8px 8px 12px; overflow: hidden; }
.table-scroll { overflow-x: auto; }
.ip-table { width: 100%; border-collapse: collapse; font-size: 13px; }

/* The header is one solid band rather than two rows of loose text: a tinted
   surface with rounded top corners, sticky so it stays legible while the reader
   scrolls a long page, and a firm rule where it meets the rows. Inside it the
   window names sit in their own chips over the columns they cover. */
.ip-table thead th {
  position: sticky; top: 0; z-index: 2;
  background: #f5f8fd;
}
.band th { padding: 12px 0 2px; border: 0; }
.band-label { text-align: center; }
.band-label span {
  display: inline-block; margin: 0 6px; padding: 3px 12px;
  border-radius: 999px; background: #e3ecfb; color: #35507d;
  font-size: 10px; font-weight: 800; letter-spacing: 0.09em; text-transform: uppercase;
}
.heads th {
  padding: 8px 8px 11px; color: #63708a; font-size: 11px; font-weight: 800;
  letter-spacing: 0.05em; text-transform: uppercase; text-align: left; white-space: nowrap;
  box-shadow: inset 0 -1px 0 #dde5f2;
}
.band th:first-child { border-top-left-radius: 14px; }
.band th:last-child { border-top-right-radius: 14px; }
.heads th.num { text-align: right; }
/* Groups are separated by air, not by lines. */
.ip-table th.lead, .ip-table td.lead { padding-left: 18px; }
.sortable { cursor: pointer; user-select: none; transition: color 0.15s, background 0.15s; }
.sortable .glyph { font-size: 13px; font-weight: 700; }
.sortable:hover { color: var(--blue); background: #eaf1fd; }
.sortable.sorted { color: var(--blue); }
.caret { display: inline-block; width: 10px; font-size: 9px; }
.caret.up { display: inline-block; transform: rotate(180deg); }

.ip-table td { padding: 10px 8px; border-top: 1px solid var(--line); white-space: nowrap; }
.ip-row { cursor: pointer; transition: background 0.15s; }
.ip-row:hover { background: #f6f9fd; }
.rank { width: 30px; color: var(--muted); font-weight: 750; font-variant-numeric: tabular-nums; text-align: right; padding-right: 4px; }

/* The address cell doubles as the rank bar: a tint sized to the row's share of
   the leading value, so the shape of the distribution is visible without a
   column of its own. */
.address { position: relative; font-weight: 750; font-variant-numeric: tabular-nums; }
.address::before {
  content: ""; position: absolute; left: 4px; top: 4px; bottom: 4px;
  width: var(--share); border-radius: 5px;
  background: linear-gradient(90deg, rgba(37, 99, 235, 0.13), rgba(37, 99, 235, 0.03));
}
.address span { position: relative; }
.country, .place, .nodes { color: var(--muted); font-weight: 600; overflow: hidden; text-overflow: ellipsis; }
.country { max-width: 158px; }
.place { max-width: 116px; }
.nodes { max-width: 132px; }
.country { display: flex; align-items: center; gap: 7px; }
.flag { font-size: 15px; line-height: 1; flex-shrink: 0; }
.country-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.node-chip {
  display: inline-block; margin-right: 4px; padding: 2px 7px;
  border-radius: 999px; background: #f0f4f9; color: #5f6b7e;
  font-size: 11px; font-weight: 700;
}
.num { text-align: right; font-variant-numeric: tabular-nums; color: #5f6b7e; }
.num.strong { font-weight: 800; color: var(--text); }
td.num.sorted { color: var(--blue); }
</style>
