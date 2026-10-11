<script setup lang="ts" generic="T extends string">
import { computed } from "vue";

// A choice between a few options, drawn as the dashboard's toggle group but
// with the active surface as one thumb that slides between them instead of
// jumping from button to button. The options share the width equally, which is
// what lets the thumb's position be a plain multiple of its own width.
const props = defineProps<{
  modelValue: T;
  options: { value: T; label: string }[];
  // What is being chosen, for a screen reader: the buttons alone say only the
  // options.
  label: string;
}>();
const emit = defineEmits<{ "update:modelValue": [value: T] }>();

const index = computed(() => Math.max(0, props.options.findIndex((o) => o.value === props.modelValue)));
</script>

<template>
  <div class="segmented" role="group" :aria-label="label" :style="{ '--count': options.length, '--index': index }">
    <span class="segmented-thumb" aria-hidden="true"></span>
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      :class="{ active: option.value === modelValue }"
      :aria-pressed="option.value === modelValue"
      @click="emit('update:modelValue', option.value)"
    >
      {{ option.label }}
    </button>
  </div>
</template>

<style scoped>
.segmented {
  --gap: 2px;
  --pad: 3px;
  position: relative; isolation: isolate;
  display: inline-grid; grid-auto-flow: column; grid-auto-columns: 1fr;
  gap: var(--gap); padding: var(--pad);
  border: 1px solid var(--line); border-radius: 10px; background: var(--surface-2);
}
/* The thumb is one element moved by transform, so a change of option is a
   slide on the compositor rather than two buttons swapping backgrounds. */
.segmented-thumb {
  position: absolute; z-index: 0;
  top: var(--pad); bottom: var(--pad); left: var(--pad);
  width: calc((100% - 2 * var(--pad) - (var(--count) - 1) * var(--gap)) / var(--count));
  border-radius: 7px; background: var(--surface-solid);
  box-shadow: var(--shadow), inset 0 0 0 1px var(--line);
  transform: translateX(calc(var(--index) * (100% + var(--gap))));
  transition: transform 0.32s var(--ease);
}
.segmented button {
  position: relative; z-index: 1;
  min-width: 0; border: none; border-radius: 7px; background: transparent; padding: 7px 16px;
  color: var(--muted); font: inherit; font-size: 12.5px; font-weight: 700; line-height: 1.2;
  white-space: nowrap; cursor: pointer;
  transition: color var(--dur) ease, transform var(--dur) var(--ease);
}
.segmented button.active { color: var(--accent); }
.segmented button:hover:not(.active) { color: var(--text); }
.segmented button:active { transform: scale(0.97); transition-duration: 60ms; }
@container app (max-width: 759px) {
  .segmented button { padding: 7px 12px; font-size: 12px; }
}
@media (pointer: coarse) {
  .segmented button { min-height: 38px; }
}
</style>
