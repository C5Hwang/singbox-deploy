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
  border: 1px solid var(--line); border-radius: 10px; padding: 8px 12px;
  background: var(--surface-2); color: var(--muted);
  font: inherit; font-size: 12.5px; font-weight: 700; line-height: 1;
  cursor: pointer; white-space: nowrap;
  transition: background-color var(--dur) ease, color var(--dur) ease, border-color var(--dur) ease;
}
.pa-toggle svg { width: 15px; height: 15px; flex-shrink: 0; }
.pa-toggle:hover:not(.active) { color: var(--text); border-color: var(--line-strong); }
/* On is the accent, the same way every other toggle on the dashboard says on. */
.pa-toggle.active {
  background: var(--accent-soft); color: var(--accent);
  border-color: var(--accent-border);
}
@container app (max-width: 759px) {
  .pa-toggle { padding: 7px 10px; font-size: 12px; }
  .pa-toggle span { display: none; }
}
</style>
