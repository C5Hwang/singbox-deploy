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
  document.addEventListener("click", onDocumentClick);
  document.addEventListener("keydown", onKeydown);
});
onUnmounted(() => {
  if (refreshTimer) window.clearInterval(refreshTimer);
  document.removeEventListener("click", onDocumentClick);
  document.removeEventListener("keydown", onKeydown);
});

const cycleLabel = computed(() => (cycleStart.value > 0 ? formatDateTime(cycleStart.value * 1000) : ""));

// The node picker is the same control as the clock in the page corner, so it
// behaves the same way too: click outside or press Escape to dismiss it.
const nodeMenuOpen = ref(false);
const nodeMenuRef = ref<HTMLElement>();

const selectedNodeLabel = computed(() => {
  if (selected.value === ALL_NODES) return "All nodes";
  const source = sources.value.find((s) => sourceKey(s) === selected.value);
  return source?.name ?? selected.value;
});

function chooseNode(value: string) {
  selected.value = value;
  nodeMenuOpen.value = false;
}

function onDocumentClick(e: MouseEvent) {
  if (nodeMenuOpen.value && nodeMenuRef.value && !nodeMenuRef.value.contains(e.target as Node)) {
    nodeMenuOpen.value = false;
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") nodeMenuOpen.value = false;
}

// A node that disappears from the fleet would otherwise leave the table filtered
// to nothing with a chip naming a node that is gone.
watch(sources, (list) => {
  if (selected.value !== ALL_NODES && !list.some((s) => sourceKey(s) === selected.value)) {
    selected.value = ALL_NODES;
  }
});

// The modal drills into whichever nodes the table is currently showing, so the
// chart it opens describes the same numbers the row does.
const modalSources = computed(() =>
  selected.value === ALL_NODES ? sources.value.map(sourceKey) : [selected.value],
);
</script>

<template>
  <section class="grid">
    <article class="card span-12 topips-head">
      <div class="head-figures">
        <p class="metric-value small">{{ rows.length }} client address{{ rows.length === 1 ? "" : "es" }}</p>
        <p v-if="cycleLabel" class="metric-detail">Cycle from {{ cycleLabel }}</p>
      </div>

      <!-- Same dropdown as the clock in the corner: a chip carrying the current
           value, and a popover headed by what is being chosen. -->
      <div ref="nodeMenuRef" class="menu-picker">
        <button
          class="chip menu-chip"
          :class="{ open: nodeMenuOpen }"
          aria-haspopup="listbox"
          :aria-expanded="nodeMenuOpen"
          aria-label="Node"
          @click="nodeMenuOpen = !nodeMenuOpen"
        >
          {{ selectedNodeLabel }}
          <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path d="M4 6l4 4 4-4" />
          </svg>
        </button>

        <div v-if="nodeMenuOpen" class="menu-pop" role="listbox" aria-label="Node">
          <div class="menu-pop-head">Node</div>
          <div class="menu-pop-list">
            <button
              class="menu-option"
              role="option"
              :aria-selected="selected === ALL_NODES"
              :class="{ active: selected === ALL_NODES }"
              @click="chooseNode(ALL_NODES)"
            >
              All nodes
              <span class="menu-note">{{ sources.length }}</span>
            </button>
            <button
              v-for="source in sources"
              :key="sourceKey(source)"
              class="menu-option"
              role="option"
              :aria-selected="selected === sourceKey(source)"
              :class="{ active: selected === sourceKey(source) }"
              @click="chooseNode(sourceKey(source))"
            >
              {{ source.name }}
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
          <colgroup>
            <col class="c-rank" />
            <col class="c-address" />
            <col class="c-country" />
            <col class="c-place" />
            <col v-if="selected === ALL_NODES" class="c-nodes" />
            <col v-for="n in 9" :key="n" class="c-num" />
          </colgroup>
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
                  <span class="sort-chip">
                    <span class="glyph">{{ d.glyph }}</span>
                    <span class="caret" :class="{ up: isSorted(w.key, d.key) && !sort.descending }">{{ isSorted(w.key, d.key) ? "▾" : "" }}</span>
                  </span>
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
/* The count is the headline and the cycle is its footnote; the picker sits
   opposite, centred against the pair rather than pinned to the baseline of a
   label it no longer has.

   The card is raised because every .card carries an animation that affects
   transform, which makes it a stacking context: without this the picker's
   popover is trapped inside this card and the table card — a later sibling —
   paints straight over it, however high the popover's own z-index is. */
.topips-head {
  position: relative; z-index: 5;
  display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 16px;
}
.head-figures { min-width: 0; }
.head-figures .metric-detail { margin-top: 5px; }
.table-card { padding: 8px 8px 12px; overflow: hidden; }
.table-scroll { overflow-x: auto; }
/* Fixed layout so the colgroup widths below are the layout, not a hint the
   browser may overrule: with auto layout a long country name simply widened its
   column and pushed the last group of numbers off the card. */
.ip-table { width: 100%; table-layout: fixed; border-collapse: collapse; font-size: 13px; }

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
  display: inline-block; padding: 3px 12px;
  border-radius: 999px; background: #e3ecfb; color: #35507d;
  font-size: 10px; font-weight: 800; letter-spacing: 0.09em; text-transform: uppercase;
}
.heads th {
  padding: 8px 7px 11px; color: #63708a; font-size: 11px; font-weight: 800;
  letter-spacing: 0.05em; text-transform: uppercase; text-align: left; white-space: nowrap;
  box-shadow: inset 0 -1px 0 #dde5f2;
}
.band th:first-child { border-top-left-radius: 14px; }
.band th:last-child { border-top-right-radius: 14px; }
.heads th.num { text-align: right; }
/* Groups are separated by air, not by lines. */
.ip-table th.lead, .ip-table td.lead { padding-left: 14px; }
/* The nine numeric columns share one width, so a group's chip sits over the
   middle of its own three rather than drifting toward whichever column happens
   to hold the widest number. */
.c-num { width: 62px; }
.c-address { width: 132px; }
.c-rank { width: 34px; }
.c-country { width: 126px; }
.c-place { width: 98px; }
.c-nodes { width: 112px; }

/* The sort affordance is a chip around the glyph, not a fill of the whole cell.
   Tinting the cell painted a wide block that started nowhere in particular and
   ran to the table's edge, under a group label centred somewhere else; a chip
   is the size of the thing it marks. */
.sortable { cursor: pointer; user-select: none; }
.sort-chip {
  display: inline-flex; align-items: center; gap: 2px;
  padding: 4px 7px; border-radius: 8px;
  transition: background 0.15s, color 0.15s;
}
.sortable .glyph { font-size: 13px; font-weight: 700; line-height: 1; }
.sortable:hover .sort-chip { background: #e6eefb; color: var(--blue); }
.sortable.sorted .sort-chip { background: var(--blue); color: white; }
.caret { display: inline-block; width: 8px; font-size: 9px; }
.caret.up { display: inline-block; transform: rotate(180deg); }

/* Same vocabulary as the sidebar's active item and the filter chips: white
   surface, hairline border, a soft blue wash for the current page rather than a
   solid block that shouts louder than the table it belongs to. */
.pager {
  display: flex; align-items: center; justify-content: center; gap: 6px;
  margin: 16px 0 4px; padding-top: 16px; border-top: 1px solid var(--line);
}
.page-num, .page-step {
  min-width: 34px; height: 34px; padding: 0 10px;
  display: inline-flex; align-items: center; justify-content: center;
  border: 1px solid var(--line); border-radius: 11px;
  background: white; color: #5f6b7e;
  font: inherit; font-size: 13px; font-weight: 700; font-variant-numeric: tabular-nums;
  cursor: pointer; transition: background 0.15s, color 0.15s, border-color 0.15s;
}
.page-step { font-size: 17px; line-height: 1; }
.page-num:hover:not(.on), .page-step:hover:not(:disabled) { background: #f6f9fd; color: var(--text); }
.page-num.on {
  background: #edf4ff; color: var(--blue);
  border-color: color-mix(in srgb, var(--blue), transparent 55%);
}
.page-step:disabled { color: #ccd4e2; border-color: #eef2f7; cursor: default; }
.page-gap { color: var(--muted); font-size: 13px; font-weight: 700; padding: 0 2px; }

.ip-table td { padding: 10px 7px; border-top: 1px solid var(--line); white-space: nowrap; }
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
.address, .num { overflow: hidden; text-overflow: ellipsis; }
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
