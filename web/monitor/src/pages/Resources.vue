<script setup lang="ts">
import { ref, computed } from "vue";
import ResourceSourceCard from "../components/ResourceSourceCard.vue";
import ResourceModal from "../components/ResourceModal.vue";
import { formatRate, tone, barStyle } from "../utils";
import type { Summary, SourceSummary } from "../types";

const props = defineProps<{ summary: Summary | null; error: string }>();
const modalSource = ref<SourceSummary | null>(null);

const sources = computed<SourceSummary[]>(() => {
  const s = props.summary;
  if (!s) return [];
  if (s.sources && s.sources.length > 0) return s.sources;
  return [{ ...s, name: "Local Server" }];
});

const localRes = computed(() => props.summary?.resources);

function fmtPct(v: number | undefined | null): string {
  if (v === undefined || v === null) return "NA";
  return `${v.toFixed(1)}%`;
}
</script>

<template>
  <section class="grid">
    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">CPU</p>
          <p class="metric-value">{{ fmtPct(localRes?.cpuPct) }}</p>
        </div>
        <span :class="`delta${tone(localRes?.cpuPct ?? null)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(localRes?.cpuPct ?? null, 'var(--blue)')"></div>
    </article>

    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Memory</p>
          <p class="metric-value">{{ fmtPct(localRes?.memPct) }}</p>
        </div>
        <span :class="`delta${tone(localRes?.memPct ?? null)}`">Live</span>
      </div>
      <div class="progress" :style="barStyle(localRes?.memPct ?? null, 'var(--cyan)')"></div>
    </article>

    <article class="card metric-card span-4">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Disk Free</p>
          <p class="metric-value">{{ fmtPct(localRes?.diskRemainingPct) }}</p>
        </div>
        <span class="delta">Live</span>
      </div>
      <div class="progress" :style="barStyle(localRes ? (100 - localRes.diskRemainingPct) : null, 'var(--green)')"></div>
    </article>
  </section>

  <section class="grid" style="margin-top: 12px;">
    <article class="card metric-card span-6">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Disk IO Read</p>
          <p class="metric-value small">{{ formatRate(localRes?.diskIOReadRate) }}</p>
        </div>
      </div>
    </article>
    <article class="card metric-card span-6">
      <div class="metric-head">
        <div>
          <p class="eyebrow">Disk IO Write</p>
          <p class="metric-value small">{{ formatRate(localRes?.diskIOWriteRate) }}</p>
        </div>
      </div>
    </article>
  </section>

  <section class="grid sources" aria-label="resource sources">
    <ResourceSourceCard
      v-for="source in sources"
      :key="source.name"
      :source="source"
      @click="modalSource = source"
    />
  </section>

  <ResourceModal
    v-if="modalSource"
    :source="modalSource"
    @close="modalSource = null"
  />
</template>
