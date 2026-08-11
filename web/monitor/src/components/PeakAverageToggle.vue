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
    :aria-pressed="modelValue"
    @click="emit('update:modelValue', !modelValue)"
  >
    <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M3 17l5-6 4 4 3-4 6 6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
      <path class="pa-rule" d="M3 12h18" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-dasharray="3 3" />
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
/* The dashed rule is the thing the button switches on, so it is the thing that
   moves: it slides up into place and settles rather than blinking on. */
.pa-rule { opacity: 0; transform: translateY(4px); transition: opacity 0.22s ease, transform 0.28s cubic-bezier(0.2, 0.8, 0.2, 1); }
.pa-toggle:hover:not(.active) { background: #f0f4f8; color: var(--text); }
.pa-toggle.active {
  background: #edf4ff; color: var(--blue);
  border-color: color-mix(in srgb, var(--blue), transparent 55%);
}
.pa-toggle.active .pa-rule { opacity: 1; transform: translateY(0); }
@media (prefers-reduced-motion: reduce) {
  .pa-toggle, .pa-rule { transition: none; }
}
@media (max-width: 720px) {
  .pa-toggle { padding: 7px 11px; font-size: 12px; }
  .pa-toggle span { display: none; }
}
</style>
