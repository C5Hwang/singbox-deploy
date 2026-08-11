<script setup lang="ts">
// The peak/average overlay switch. It sits beside the granularity buttons on
// every trend chart and reads as one of them until it is on, at which point it
// takes the accent so the annotated chart explains itself.
defineProps<{ modelValue: boolean }>();
const emit = defineEmits<{ "update:modelValue": [value: boolean] }>();
</script>

<template>
  <button
    class="pa-toggle"
    :class="{ active: modelValue }"
    type="button"
    aria-label="Peak / Avg"
    :aria-pressed="modelValue"
    @click="emit('update:modelValue', !modelValue)"
  >
    <!-- A single rise and fall. The icon used to carry a dashed rule across it
         for the average as well, but at 15 px the dashes are a pixel each and
         land on the line they were meant to sit behind, so the two read as one
         smudge. One shape that is unmistakably a peak beats two that are not
         legible at the size they are drawn. -->
    <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M3 17l5-2 4-9 4 9 5-2" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
    </svg>
    <span>Peak / Avg</span>
  </button>
</template>

<style scoped>
.pa-toggle {
  display: inline-flex; align-items: center; gap: 7px;
  border: 1px solid var(--line); border-radius: 10px; padding: 8px 13px;
  background: white; color: var(--muted);
  font: inherit; font-size: 13px; font-weight: 700; line-height: 1;
  cursor: pointer; white-space: nowrap;
  transition: background 0.18s ease, color 0.18s ease, border-color 0.18s ease;
}
.pa-toggle svg { width: 15px; height: 15px; flex-shrink: 0; }
.pa-toggle:hover:not(.active) { background: #f0f4f8; color: var(--text); }
/* On is the accent, the same way every other toggle on the dashboard says on. */
.pa-toggle.active {
  background: #edf4ff; color: var(--blue);
  border-color: color-mix(in srgb, var(--blue), transparent 55%);
}
@media (prefers-reduced-motion: reduce) {
  .pa-toggle { transition: none; }
}
@media (max-width: 720px) {
  .pa-toggle { padding: 7px 11px; font-size: 12px; }
  .pa-toggle span { display: none; }
}
</style>
