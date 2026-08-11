<script setup lang="ts">
import { computed } from "vue";
import type { LatencySnapshot, PingLatestPoint, PingTarget } from "../types";

// One carrier per row, one city per column: the two comparisons an operator
// actually makes — this carrier across the country, this city across carriers —
// are a row and a column rather than a search through nine text lines.
const props = defineProps<{ snapshot: LatencySnapshot }>();

// Latency buckets on a green-to-red ramp, in the bright register ping tools
// use: green reads as "fine" at a glance and red as "look at this", which is
// the whole job of the colour here.
//
// Red and green are exactly the pair red-green colour blindness collapses. A
// bright traffic-light ramp cannot also be monotone in lightness — yellow is
// the lightest hue there is, so it will always sit above green — so the second
// reading is carried by the number printed on every cell rather than by
// brightness. Ink is chosen per step and clears 4.5:1 on all of them, so the
// numbers stay legible whatever the fill.
const BUCKETS = [
  { limit: 150, fill: "#4ade80", ink: "#0b3a1e" },
  { limit: 250, fill: "#fbbf24", ink: "#3d2a00" },
  { limit: 350, fill: "#fb7a3c", ink: "#4a1a02" },
  { limit: Infinity, fill: "#f2544f", ink: "#330807" },
];
// A probe that answered nothing is not "slow", it is out — off the end of the
// ramp rather than at the far end of it, so it takes the one solid fill.
const DEAD = { fill: "#dc2626", ink: "#ffffff" };
const MISSING = { fill: "#f1f4f9", ink: "#98a2b3" };

// Loss is a state, not a magnitude, so it takes reserved colours rather than a
// step of the latency ramp. On these brighter fills the bar needs to be darker
// than the cell to register, which inverts what it was on the old dark ramp.
const LOSS_WARNING = "#b45309";
const LOSS_CRITICAL = "#7f1d1d";

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

function latestAt(carrier: string, city: string): PingLatestPoint | undefined {
  const target = props.snapshot.targets.find((t) => t.carrier === carrier && t.city === city);
  return target ? props.snapshot.latest.find((p) => p.target === target.id) : undefined;
}

interface Cell {
  key: string;
  text: string;
  loss: number;
  hasLoss: boolean;
  style: Record<string, string>;
  title: string;
}

function cell(carrier: string, city: string): Cell {
  const latest = latestAt(carrier, city);
  const ms = latest?.avgMs ?? null;
  const loss = latest?.lossPct ?? 0;
  const tone = ms === null ? (latest ? DEAD : MISSING) : BUCKETS.find((b) => ms < b.limit)!;
  const text = ms === null ? (latest ? "out" : "—") : ms >= 100 ? String(Math.round(ms)) : ms.toFixed(1);
  return {
    key: `${carrier}-${city}`,
    text,
    loss,
    hasLoss: !!latest && loss > 0,
    style: {
      "--fill": tone.fill,
      "--ink": tone.ink,
      "--loss": `${Math.max(0, Math.min(100, loss))}%`,
      "--loss-color": loss >= 50 ? LOSS_CRITICAL : LOSS_WARNING,
    },
    title: `${carrier} · ${city} — ${ms === null ? "no answer" : `${text} ms`}, ${latest ? `${Math.round(loss)}% loss` : "no data"}`,
  };
}

// Carrier names are long enough to crowd a half-width card; the matrix only
// needs enough to tell three of them apart.
function shortCarrier(name: string): string {
  return name.replace(/^China\s+/, "");
}
</script>

<template>
  <div class="matrix" role="table" aria-label="Latency by carrier and city, milliseconds">
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
        :style="cell(carrier, city).style"
        :title="cell(carrier, city).title"
        role="cell"
      >
        <span class="value">{{ cell(carrier, city).text }}</span>
        <!-- The bar is only drawn when something was lost, so a healthy matrix
             stays clean and any bar at all is the thing that catches the eye. -->
        <i v-if="cell(carrier, city).hasLoss" class="loss" aria-hidden="true"></i>
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
  position: relative; overflow: hidden;
  display: grid; place-items: center;
  height: 44px; border-radius: 10px;
  background: var(--fill); color: var(--ink);
  font-size: 14px; font-weight: 800; font-variant-numeric: tabular-nums;
  cursor: default;
}
.value { line-height: 1; }
/* A track the width of the cell with the lost fraction filled in, so the bar
   reads as a proportion rather than as an arbitrary stub. The track is a dark
   wash so it holds on the light green step as well as on the dark red one. */
.loss {
  position: absolute; left: 6px; right: 6px; bottom: 5px; height: 4px;
  border-radius: 999px; background: rgba(0, 0, 0, 0.13);
}
.loss::after {
  content: ""; position: absolute; inset: 0 auto 0 0;
  width: var(--loss); min-width: 4px;
  border-radius: inherit; background: var(--loss-color);
}
@media (max-width: 720px) {
  .row { grid-template-columns: 52px repeat(3, minmax(0, 1fr)); }
  .cell { height: 40px; font-size: 13px; }
}
</style>
