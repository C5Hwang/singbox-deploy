<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import IPTrendModal from "../components/IPTrendModal.vue";
import { fetchIPTraffic, ipDetailKey } from "../api";
import { flagFor, locations, resolveLocations } from "../geo";
import { formatBytesCompact, formatDateTime } from "../utils";
import type {
  IPDirectionKey,
  IPTrafficEntry,
  IPTrafficRow,
  IPTrafficSegment,
  IPTrafficSnapshot,
  IPTrafficWindow,
  IPWindowKey,
  IPSort,
  Summary,
} from "../types";

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
const modalRow = ref<IPTrafficRow | null>(null);
// modalSegment narrows the chart to one strand of the row: a landing node's
// forwarded traffic, or the address's direct traffic. Null charts the whole row.
const modalSegment = ref<IPTrafficSegment | null>(null);
// Which addresses are showing their breakdown. Keyed by address rather than by
// row index, so re-sorting or a refresh does not move the disclosure onto a
// different client.
const expanded = ref(new Set<string>());

// What each node has answered: its snapshot, or null for a node that could not
// be read. A node missing from the map has not answered yet. Keeping the answers
// rather than one merged table is what lets the ranking be rebuilt from the
// nodes that have replied while the rest are still in flight.
interface NodeAnswer {
  name: string;
  snapshot: IPTrafficSnapshot | null;
}
const answers = ref(new Map<string, NodeAnswer>());
let round = 0;

// The nodes the table is currently showing.
const targets = computed(() =>
  selected.value === ALL_NODES ? sources.value : sources.value.filter((s) => sourceKey(s) === selected.value),
);
const answered = computed(() =>
  targets.value
    .map((source) => answers.value.get(sourceKey(source)))
    .filter((answer): answer is NodeAnswer => answer !== undefined),
);
const pending = computed(() => answered.value.length < targets.value.length);

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

// segmentKey groups a node's entries into the strands a row is built from: one
// for direct traffic and one per landing node. A node that predates per-landing
// accounting reports relayed traffic with no landing, which becomes a strand of
// its own rather than being folded into a named one it may not belong to.
function segmentKey(entry: IPTrafficEntry): string {
  if (!entry.relayed) return "direct";
  return `relay:${entry.landing ?? ""}`;
}

function segmentLabel(entry: IPTrafficEntry): string {
  if (!entry.relayed) return "Direct";
  return entry.landingName || entry.landing || "Unknown landing";
}

// Nodes are queried in parallel and each answer is folded in as it lands, so a
// node that is powered off — which can only fail by timing out — does not hold
// the table back while the rest of the fleet is already known.
function load() {
  const list = targets.value;
  if (list.length === 0) return;
  const current = ++round;
  for (const source of list) {
    const key = sourceKey(source);
    const name = source.name ?? key;
    fetchIPTraffic(key)
      .then((snapshot): NodeAnswer => ({ name, snapshot }))
      .catch((): NodeAnswer => ({ name, snapshot: null }))
      .then((answer) => {
        if (current === round) answers.value.set(key, answer);
      });
  }
}

// Addresses are merged across nodes and across everything those nodes did for
// them: the same client reaching two nodes directly and being relayed to a third
// is one row whose totals are the sum, which is what a "top 30" over a fleet has
// to mean. What the sum is made of survives as segments — direct traffic, and
// one strand per landing node — so the row can be taken apart again without a
// second request.
const rows = computed<IPTrafficRow[]>(() => {
  const merged = new Map<string, { row: IPTrafficRow; segments: Map<string, IPTrafficSegment> }>();
  for (const { name, snapshot } of answered.value) {
    if (!snapshot) continue;
    for (const entry of snapshot.entries) {
      let group = merged.get(entry.ip);
      if (!group) {
        group = {
          row: {
            ip: entry.ip,
            relayed: false,
            nodes: [],
            segments: [],
            cycle: emptyWindow(),
            today: emptyWindow(),
            last7: emptyWindow(),
          },
          segments: new Map(),
        };
        merged.set(entry.ip, group);
      }
      const { row, segments } = group;
      row.relayed ||= entry.relayed ?? false;
      if (!row.nodes.includes(name)) row.nodes.push(name);
      addWindow(row.cycle, entry.cycle);
      addWindow(row.today, entry.today);
      addWindow(row.last7, entry.last7);

      const key = segmentKey(entry);
      let segment = segments.get(key);
      if (!segment) {
        segment = {
          relayed: entry.relayed ?? false,
          landing: entry.landing ?? "",
          label: segmentLabel(entry),
          nodes: [],
          cycle: emptyWindow(),
          today: emptyWindow(),
          last7: emptyWindow(),
        };
        segments.set(key, segment);
      }
      // The name travels with the traffic, not with the key: one node may know
      // a landing node's alias while another, asked a moment earlier, did not.
      if (entry.landingName) segment.label = entry.landingName;
      if (!segment.nodes.includes(name)) segment.nodes.push(name);
      addWindow(segment.cycle, entry.cycle);
      addWindow(segment.today, entry.today);
      addWindow(segment.last7, entry.last7);
    }
  }
  return [...merged.values()].map(({ row, segments }) => {
    // Direct traffic leads, then the landing nodes by name, so a row's
    // breakdown reads the same way on every refresh.
    row.segments = [...segments.values()].sort((a, b) => {
      if (a.relayed !== b.relayed) return a.relayed ? 1 : -1;
      return a.label.localeCompare(b.label);
    });
    return row;
  });
});

// segmentDetailKey is the wire key one strand's own history is stored under.
function segmentDetailKey(row: IPTrafficRow, segment: IPTrafficSegment): string {
  return ipDetailKey({ ip: row.ip, relayed: segment.relayed, landing: segment.landing });
}

// How much of a row the relay carried, in whichever cell the table is sorted
// by. It is the number the collapsed row shows beside the address, so "this
// client was forwarded" is legible without opening the breakdown.
function relayedValue(row: IPTrafficRow): number {
  const { window, direction } = sort.value;
  return row.segments.reduce((sum, s) => (s.relayed ? sum + s[window][direction] : sum), 0);
}

// A row is only worth taking apart when it is made of more than one strand.
function expandable(row: IPTrafficRow): boolean {
  return row.segments.length > 1;
}

function toggleExpanded(row: IPTrafficRow) {
  const next = new Set(expanded.value);
  if (!next.delete(row.ip)) next.add(row.ip);
  expanded.value = next;
}

function openRow(row: IPTrafficRow, segment: IPTrafficSegment | null) {
  modalRow.value = row;
  modalSegment.value = segment;
}

const cycleStart = computed(() => Math.max(0, ...answered.value.map((a) => a.snapshot?.cycleStart ?? 0)));
const unavailableNodes = computed(() => answered.value.filter((a) => !a.snapshot).map((a) => a.name));
const disabledNodes = computed(() =>
  answered.value.filter((a) => a.snapshot && !a.snapshot.enabled).map((a) => a.name),
);
// Only a verdict once every node has reported: while one is still in flight the
// table is incomplete rather than unavailable.
const loadError = computed(() =>
  !pending.value && targets.value.length > 0 && unavailableNodes.value.length === targets.value.length
    ? "no node returned per-IP data"
    : "",
);

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

function chooseWindow(window: IPWindowKey) {
  if (sort.value.window === window) return;
  sort.value = { ...sort.value, window };
}

// The figure a card leads with, and the one every line inside it is measured
// by: whichever cell the list is currently ranked on.
function sortedValue(windows: { cycle: IPTrafficWindow; today: IPTrafficWindow; last7: IPTrafficWindow }): number {
  return windows[sort.value.window][sort.value.direction];
}

// The two directions the card is not ranked by, so a card still carries all
// three figures without repeating the one in its header.
const otherDirections = computed(() => directions.filter((d) => d.key !== sort.value.direction));

// One line for the whole place, the way the modal writes it: two columns of a
// table become one line of a card.
function cardPlace(ip: string): string {
  return placeLabel(ip) || "Location unresolved";
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
watch(() => targets.value.map(sourceKey).join(","), load, { immediate: true });

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
      <p v-if="pending && rows.length === 0" class="no-data">Loading per-IP traffic...</p>
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
            <template v-for="(row, i) in visible" :key="row.ip">
              <tr class="ip-row" @click="openRow(row, null)">
                <td class="rank">{{ firstRank + i }}</td>
                <td class="address" :style="shareStyle(row)">
                  <!-- The disclosure is a button of its own so opening the
                       breakdown and opening the chart stay two separate acts on
                       the same row. -->
                  <span v-if="!expandable(row)" class="twisty-space" aria-hidden="true"></span>
                  <button
                    v-else
                    class="twisty"
                    :class="{ open: expanded.has(row.ip) }"
                    :aria-expanded="expanded.has(row.ip)"
                    :aria-label="`Traffic breakdown for ${row.ip}`"
                    @click.stop="toggleExpanded(row)"
                  >
                    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                      <path d="M6 4l4 4-4 4" />
                    </svg>
                  </button>
                  <span class="ip">{{ row.ip }}</span>
                  <!-- The number, not just the fact: how much of this row the
                       fleet forwarded, in whichever cell the table is sorted by. -->
                  <span
                    v-if="row.relayed"
                    class="relay-chip"
                    :title="`Forwarded to a landing node: ${formatBytesCompact(relayedValue(row))}`"
                  >
                    relay {{ formatBytesCompact(relayedValue(row)) }}
                  </span>
                </td>
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
              <!-- One line per strand of the row, in the same nine columns, so
                   the split reads against the totals directly above it. -->
              <tr
                v-for="segment in expanded.has(row.ip) ? row.segments : []"
                :key="segmentDetailKey(row, segment)"
                class="ip-row sub-row"
                @click="openRow(row, segment)"
              >
                <td class="rank"></td>
                <td class="address sub-address">
                  <span class="strand" :class="{ relayed: segment.relayed }">{{ segment.relayed ? "→" : "●" }}</span>
                  <span class="strand-label">{{ segment.label }}</span>
                </td>
                <td class="country"></td>
                <td class="place"></td>
                <td v-if="selected === ALL_NODES" class="nodes">
                  <span v-for="node in segment.nodes" :key="node" class="node-chip">{{ node }}</span>
                </td>
                <template v-for="w in windows" :key="w.key">
                  <td
                    v-for="d in directions"
                    :key="`${w.key}-${d.key}`"
                    class="num"
                    :class="{ sorted: isSorted(w.key, d.key), strong: d.key === 'totalBytes', lead: d.key === 'inBytes' }"
                  >
                    {{ formatBytesCompact(segment[w.key][d.key]) }}
                  </td>
                </template>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <!-- Below the table's breakpoint the nine columns cannot be read without
           scrolling their own labels off the screen, so the same ranking is
           rendered as cards instead: one client per card, its landings inline
           and each figure beside the name it belongs to. -->
      <div v-if="visible.length" class="ip-cards">
        <div class="card-sort">
          <div class="toggle-group" role="group" aria-label="Ranking window">
            <button
              v-for="w in windows"
              :key="w.key"
              :class="{ active: sort.window === w.key }"
              :aria-pressed="sort.window === w.key"
              @click="chooseWindow(w.key)"
            >
              {{ w.label }}
            </button>
          </div>
          <div class="toggle-group" role="group" aria-label="Ranking direction">
            <button
              v-for="d in directions"
              :key="d.key"
              :class="{ active: sort.direction === d.key }"
              :title="`${d.title} — tap again to reverse`"
              :aria-label="d.title"
              @click="sortBy(sort.window, d.key)"
            >
              {{ d.glyph }}<span v-if="sort.direction === d.key" class="caret" :class="{ up: !sort.descending }">▾</span>
            </button>
          </div>
        </div>

        <article v-for="(row, i) in visible" :key="row.ip" class="ip-card">
          <button class="card-main" :aria-label="`Traffic for ${row.ip}`" @click="openRow(row, null)">
            <span class="card-head">
              <span class="card-rank">{{ firstRank + i }}</span>
              <span class="card-ip">{{ row.ip }}</span>
              <span class="card-value">{{ formatBytesCompact(sortedValue(row)) }}</span>
              <svg class="card-go" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M6 4l4 4-4 4" />
              </svg>
            </span>
            <span class="card-place">
              <span v-if="placeOf(row.ip).code" class="flag">{{ flagFor(placeOf(row.ip).code) }}</span>
              <span class="card-place-name">{{ cardPlace(row.ip) }}</span>
              <!-- Only where there is no breakdown to say it: on a card that
                   lists its landings, the lines below already do. -->
              <span v-if="row.relayed && row.segments.length < 2" class="relay-chip">relay</span>
            </span>
            <span class="card-share" :style="shareStyle(row)" aria-hidden="true"></span>
          </button>

          <button
            v-for="segment in row.segments.length > 1 ? row.segments : []"
            :key="segmentDetailKey(row, segment)"
            class="card-strand"
            :aria-label="`${segment.label} traffic for ${row.ip}`"
            @click="openRow(row, segment)"
          >
            <span class="strand" :class="{ relayed: segment.relayed }">{{ segment.relayed ? "→" : "●" }}</span>
            <span class="strand-label">{{ segment.label }}</span>
            <span class="strand-value">{{ formatBytesCompact(sortedValue(segment)) }}</span>
          </button>

          <div class="card-figures">
            <span v-for="d in otherDirections" :key="d.key" class="card-figure">
              <span class="glyph" :title="d.title">{{ d.glyph }}</span>
              {{ formatBytesCompact(row[sort.window][d.key]) }}
            </span>
            <span v-if="selected === ALL_NODES" class="card-nodes">
              <span v-for="node in row.nodes" :key="node" class="node-chip">{{ node }}</span>
            </span>
          </div>
        </article>
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

  <!-- Keyed by the strand it describes, so opening a different one builds a new
       chart rather than leaving the first one's series in place. -->
  <IPTrendModal
    v-if="modalRow"
    :key="modalSegment ? segmentDetailKey(modalRow, modalSegment) : modalRow.ip"
    :row="modalRow"
    :segment="modalSegment"
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

/* The header sits on the card's own surface rather than on a tint of its own:
   a panel a shade off the card it lives in reads as a separate slab pasted on
   top. It stays opaque because it is sticky — rows scroll under it — and it is
   the rule underneath, not a fill, that separates it from them. Inside it the
   window names sit in their own chips over the columns they cover. */
.ip-table thead th {
  position: sticky; top: 0; z-index: 2;
  background: var(--card);
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
.heads th.num { text-align: right; }
/* Groups are separated by air, not by lines. */
.ip-table th.lead, .ip-table td.lead { padding-left: 14px; }
/* The nine numeric columns share one width, so a group's chip sits over the
   middle of its own three rather than drifting toward whichever column happens
   to hold the widest number. */
.c-num { width: 62px; }
/* Wide enough for a disclosure, a full dotted quad, and the relay chip that
   carries a figure beside it. The extra room comes out of the two place
   columns, whose names ellipsize gracefully, rather than out of the numeric
   columns, which clip. */
.c-address { width: 232px; }
.c-rank { width: 34px; }
.c-country { width: 112px; }
.c-place { width: 92px; }
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
   column of its own. It stays a table cell — a flex cell would drop out of the
   column grid — so the column is simply sized for its longest content. */
.address { position: relative; font-weight: 750; font-variant-numeric: tabular-nums; }
.address::before {
  content: ""; position: absolute; left: 4px; top: 4px; bottom: 4px;
  width: var(--share); border-radius: 5px;
  background: linear-gradient(90deg, rgba(37, 99, 235, 0.13), rgba(37, 99, 235, 0.03));
}
.address .ip, .address .relay-chip, .address .twisty { position: relative; }

/* The disclosure sits in the address cell rather than in a column of its own:
   a column would be blank on every row that has nothing to open, and most rows
   have nothing to open. A row without one still reserves its width, so the
   addresses read as one column rather than as two ragged ones.

   These are qualified by the table because the cell padding they adjust is set
   by `.ip-table td`, which outranks a bare class. */
.twisty, .twisty-space {
  display: inline-flex; align-items: center; justify-content: center;
  width: 18px; height: 18px; margin-right: 3px;
  vertical-align: -4px;
}
.twisty {
  padding: 0; border: 0; border-radius: 6px;
  background: transparent; color: #93a1b8;
  cursor: pointer; transition: background 0.15s, color 0.15s, transform 0.15s;
}
.twisty svg { width: 13px; height: 13px; }
.twisty:hover { background: #e6eefb; color: var(--blue); }
.twisty.open { transform: rotate(90deg); color: var(--blue); }
.ip-table td.address { padding-left: 4px; }
/* Indented past where the addresses above them start, so a breakdown line can
   never be mistaken for a client of its own. */
.ip-table td.sub-address { padding-left: 34px; font-weight: 650; }

/* The breakdown is the same table, half a step back: no share bar, a quieter
   surface, and a hairline that ties each line to the row it came out of rather
   than separating it from one. */
.sub-row > td { background: #fafcff; border-top: 1px solid #eef3fa; }
.sub-row:hover > td { background: #f2f7fe; }
.sub-address::before { content: none; }
.strand { display: inline-block; width: 15px; color: #b3c0d4; font-weight: 800; }
.strand.relayed { color: #d3922b; }
.strand-label { color: #55637a; }
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
/* Same chip vocabulary as the window bands, in a warm tint of its own: part of
   this row is traffic the fleet forwarded rather than terminated, and the chip
   says how much of it in the same cell the table is ranked by. */
.relay-chip {
  margin-left: 5px; padding: 2px 6px;
  border-radius: 999px; background: #fdf0dc; color: #955d10;
  font-size: 9px; font-weight: 800; letter-spacing: 0.04em; text-transform: uppercase;
  font-variant-numeric: tabular-nums;
}
.num { text-align: right; font-variant-numeric: tabular-nums; color: #5f6b7e; }
.num.strong { font-weight: 800; color: var(--text); }
td.num.sorted { color: var(--blue); }

/* ── Cards ────────────────────────────────────────────────────
   The card list is the same ranking at a width where the table cannot be one.
   Only one of the two is ever in the document flow, so the page never carries
   a horizontal scroller a phone has to fight. */
.ip-cards { display: none; flex-direction: column; gap: 10px; padding: 4px; }

/* Column headers are what sorts the table; a card list has none, so the same
   sort state gets two chip groups. The window group only ever selects — tapping
   the direction already chosen is what reverses the order, so re-picking the
   window you are already on cannot silently flip the ranking. */
.card-sort { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 2px; }
.card-sort .toggle-group button { min-height: 44px; }
.card-sort .caret { width: auto; margin-left: 3px; font-size: 9px; }

.ip-card {
  display: flex; flex-direction: column;
  border: 1px solid var(--line); border-radius: 14px; background: white;
  overflow: hidden;
}
/* The head, the place and the share bar are one target: the whole summary
   opens the whole address's chart. */
.card-main {
  display: flex; flex-direction: column; gap: 7px;
  padding: 13px 14px 12px; border: 0; background: transparent;
  font: inherit; text-align: left; cursor: pointer;
  transition: background 0.15s;
}
.card-main:active { background: #f2f7fe; }
.card-head { display: flex; align-items: center; gap: 9px; }
.card-rank {
  min-width: 20px; color: var(--muted);
  font-size: 12px; font-weight: 750; font-variant-numeric: tabular-nums;
}
.card-ip {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 15px; font-weight: 780; font-variant-numeric: tabular-nums; color: var(--text);
}
.card-value {
  font-size: 15px; font-weight: 800; font-variant-numeric: tabular-nums; color: var(--blue);
}
.card-go { width: 14px; height: 14px; flex-shrink: 0; color: #b3c0d4; }
.card-place {
  display: flex; align-items: center; gap: 7px; min-width: 0;
  color: var(--muted); font-size: 12.5px; font-weight: 600;
}
.card-place-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The table draws this behind the address; a card has room to give the share of
   the ranking a rule of its own. */
.card-share {
  height: 4px; border-radius: 999px;
  background: linear-gradient(90deg, rgba(37, 99, 235, 0.55), rgba(37, 99, 235, 0.16));
  width: var(--share); min-width: 3px;
}

/* One line per landing, each its own target, so a phone can reach a single
   landing node's chart without a table cell to hit. */
.card-strand {
  display: flex; align-items: center; gap: 9px;
  min-height: 44px; padding: 0 14px;
  border: 0; border-top: 1px solid #eef3fa; background: #fafcff;
  font: inherit; text-align: left; cursor: pointer;
  transition: background 0.15s;
}
.card-strand:active { background: #eef4fd; }
.card-strand .strand-label {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: 13px; font-weight: 650;
}
.strand-value {
  font-size: 13px; font-weight: 750; font-variant-numeric: tabular-nums; color: #47536a;
}

/* The two directions the card is not ranked by. They are data, not a control,
   so they carry no press state. */
.card-figures {
  display: flex; align-items: center; flex-wrap: wrap; gap: 6px 14px;
  padding: 9px 14px 10px; border-top: 1px solid #eef3fa;
  color: var(--muted); font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums;
}
.card-figure .glyph { margin-right: 3px; font-weight: 800; }
.card-nodes { margin-left: auto; display: flex; flex-wrap: wrap; gap: 4px; }
.card-nodes .node-chip { margin-right: 0; }

@media (max-width: 720px) {
  .table-scroll { display: none; }
  .ip-cards { display: flex; }
  .table-card { padding: 8px; }
  /* The head card stacks, so its picker no longer has a row to share. */
  .topips-head { align-items: stretch; }
  .menu-picker .menu-pop { right: auto; left: 0; width: min(280px, calc(100vw - 64px)); }
}
</style>
