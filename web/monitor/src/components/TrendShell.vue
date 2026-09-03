<script setup lang="ts">
import PeakAverageToggle from "./PeakAverageToggle.vue";
import type { ModeOption, TrendMode } from "../trendModes";

// The frame every trend chart is shown in: the backdrop that closes on a click
// outside, the title and its one-line subtitle, the granularity buttons, the
// peak/average switch, and the three states the body can be in.
//
// What a chart is of belongs to the chart; how it is framed belongs here, so
// every modal words the same wait, the same error and the same controls.
//
// The chart element itself stays with the caller, in the default slot, because
// it is the caller's useTrendChart that owns the ECharts instance living in it.
defineProps<{
  title: string;
  subtitle: string;
  // A chart with nothing to choose — the latency and relay ones, which are
  // always a week of one-minute rounds — passes no modes and gets no buttons.
  modes?: ModeOption[];
  mode?: TrendMode;
  peakAverage: boolean;
  loading: boolean;
  // A whole sentence, not a code: the reader is told what is missing rather
  // than being shown the failure that lost it.
  error?: string;
}>();

const emit = defineEmits<{
  close: [];
  "update:mode": [value: TrendMode];
  "update:peakAverage": [value: boolean];
}>();
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-content" role="dialog" aria-modal="true" :aria-label="title">
      <button class="close-btn" @click="emit('close')" aria-label="Close">&times;</button>
      <div class="modal-header">
        <div>
          <h2 class="modal-title">{{ title }}</h2>
          <p class="modal-subtitle">{{ subtitle }}</p>
        </div>
        <div class="modal-controls">
          <div v-if="modes && modes.length" class="toggle-group">
            <button
              v-for="option in modes"
              :key="option.key"
              :class="{ active: mode === option.key }"
              @click="emit('update:mode', option.key)"
            >
              {{ option.label }}
            </button>
          </div>
          <PeakAverageToggle
            :modelValue="peakAverage"
            @update:modelValue="(value) => emit('update:peakAverage', value)"
          />
        </div>
      </div>

      <!-- Anything a single chart adds to its own controls: the latency modal's
           carrier and city filters are the only ones so far. -->
      <slot name="filters" />

      <div class="modal-body">
        <div v-if="loading" class="chart-loading">
          <div class="chart-wait" aria-hidden="true"><span></span><span></span><span></span></div>
          Loading trend data...
        </div>
        <div v-else-if="error" class="chart-loading">{{ error }}</div>
        <!-- Hidden rather than dropped: the chart is initialised into this
             element once, and unmounting it would take the ECharts instance
             with it. -->
        <div v-show="!loading && !error" class="chart-frame"><slot /></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Three bars that rise in turn: a chart's own shape, waiting. */
.chart-wait { display: flex; justify-content: center; align-items: flex-end; gap: 5px; height: 26px; margin-bottom: 14px; }
.chart-wait span {
  width: 6px; height: 10px; border-radius: 3px; background: var(--accent);
  animation: waitBar 1s var(--ease) infinite;
}
.chart-wait span:nth-child(2) { animation-delay: 0.15s; }
.chart-wait span:nth-child(3) { animation-delay: 0.3s; }
@keyframes waitBar { 0%, 100% { height: 8px; opacity: 0.45 } 50% { height: 24px; opacity: 1 } }
.chart-frame { animation: fadeIn 0.25s ease; }
</style>
