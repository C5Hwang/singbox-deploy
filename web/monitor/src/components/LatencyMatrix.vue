<script setup lang="ts">
import { computed } from "vue";
import type { LatencySnapshot, PingLatestPoint, PingTarget } from "../types";

// One carrier per row, one city per column: the two comparisons an operator
// actually makes — this carrier across the country, this city across carriers —
// are a row and a column rather than a search through nine text lines.
const props = defineProps<{ snapshot: LatencySnapshot }>();

// Latency buckets, not a continuous ramp. The cells are discrete tiles with the
// value printed on them, so an ordinal scale is the honest encoding: one hue,
// light to dark, four steps whose lightest still stands off the card surface.
// Steps are the documented blue ramp at 250 / 400 / 500 / 650.
const BUCKETS = [
  { limit: 150, fill: "#86b6ef", ink: "#0d2a52" },
  { limit: 250, fill: "#3987e5", ink: "#ffffff" },
  { limit: 350, fill: "#256abf", ink: "#ffffff" },
  { limit: Infinity, fill: "#104281", ink: "#ffffff" },
];
// A probe that answered nothing is not "slow", it is out. It gets the surface
// and the critical status colour rather than the far end of the latency ramp.
const DEAD = { fill: "#fdf0f0", ink: "#b91c1c" };
const MISSING = { fill: "#f4f6fa", ink: "#98a2b3" };

const carriers = computed(() => unique((t) => t.carrier));
const cities = computed(() => unique((t) => t.city));

function unique(pick: (t: PingTarget) => string): string[] {
  const seen: string[] = [];
  for (const target of props.snapshot.targets) {
    const value = pick(target);
    if (!seen.includes(value)) seen.push(value);
  }
  return seen;
}

function targetAt(carrier: string, city: string): PingTarget | undefined {
  return props.snapshot.targets.find((t) => t.carrier === carrier && t.city === city);
}

function latestAt(carrier: string, city: string): PingLatestPoint | undefined {
  const target = targetAt(carrier, city);
  return target ? props.snapshot.latest.find((p) => p.target === target.id) : undefined;
}

interface Cell {
  key: string;
  text: string;
  loss: number | null;
  style: Record<string, string>;
  title: string;
}

function cell(carrier: string, city: string): Cell {
  const latest = latestAt(carrier, city);
  const key = `${carrier}-${city}`;
  const ms = latest?.avgMs ?? null;
  const loss = latest ? latest.lossPct : null;
  const tone = ms === null ? (latest ? DEAD : MISSING) : BUCKETS.find((b) => ms < b.limit)!;
  const text = ms === null ? (latest ? "out" : "—") : ms >= 100 ? String(Math.round(ms)) : ms.toFixed(1);
  const lossText = loss === null ? "no data" : `${Math.round(loss)}% loss`;
  return {
    key,
    text,
    loss,
    style: { "--fill": tone.fill, "--ink": tone.ink },
    title: `${carrier} · ${city} — ${ms === null ? "no answer" : `${text} ms`}, ${lossText}`,
  };
}

// Carrier and city names are long enough to crowd a half-width card; the matrix
// only needs enough to tell three of each apart.
function shortCarrier(name: string): string {
  return name.replace(/^China\s+/, "");
}
</script>

<template>
  <div class="matrix" role="table" :aria-label="`Latency by carrier and city, milliseconds`">
    <div class="row head" role="row">
      <span class="corner" role="columnheader"></span>
      <span v-for="city in cities" :key="city" class="col-head" role="columnheader">{{ city }}</span>
    </div>
    <div v-for="carrier in carriers" :key="carrier" class="row" role="row">
      <span class="row-head" role="rowheader">{{ shortCarrier(carrier) }}</span>
      <span
        v-for="city in cities"
        :key="cell(carrier, city).key"
        class="cell"
        :class="{ lossy: (cell(carrier, city).loss ?? 0) > 0 }"
        :style="cell(carrier, city).style"
        :title="cell(carrier, city).title"
        role="cell"
      >
        {{ cell(carrier, city).text }}
      </span>
    </div>
  </div>
</template>

<style scoped>
.matrix { display: flex; flex-direction: column; gap: 5px; margin-top: 16px; }
.row { display: grid; grid-template-columns: 62px repeat(3, minmax(0, 1fr)); gap: 5px; align-items: center; }
.col-head, .row-head {
  color: var(--muted); font-size: 11px; font-weight: 750;
  letter-spacing: 0.03em; text-transform: uppercase;
}
.col-head { text-align: center; padding-bottom: 2px; }
.row-head { text-align: right; padding-right: 2px; }
.cell {
  position: relative;
  display: grid; place-items: center;
  height: 42px; border-radius: 10px;
  background: var(--fill); color: var(--ink);
  font-size: 14px; font-weight: 800; font-variant-numeric: tabular-nums;
  cursor: default;
}
/* Loss is a state, not a magnitude, so it never rides the latency ramp: it is
   a corner marker on top of whatever the latency colour already said. */
.cell.lossy::after {
  content: ""; position: absolute; top: 5px; right: 5px;
  width: 6px; height: 6px; border-radius: 999px;
  background: #fab219; box-shadow: 0 0 0 2px color-mix(in srgb, var(--fill), transparent 40%);
}
@media (max-width: 720px) {
  .row { grid-template-columns: 52px repeat(3, minmax(0, 1fr)); }
  .cell { height: 38px; font-size: 13px; }
}
</style>
