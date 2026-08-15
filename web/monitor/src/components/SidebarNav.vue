<script setup lang="ts">
import type { Tab } from "../types";

defineProps<{
  activeTab: Tab;
  sourceCount: number;
  // showRelay is false on a fleet where nothing is relayed, which is most of
  // them: an entry that only ever leads to an empty page is worse than no entry.
  showRelay: boolean;
}>();
defineEmits<{
  "update:activeTab": [value: Tab];
}>();
</script>

<template>
  <aside class="sidebar" aria-label="Sidebar navigation">
    <div class="brand">
      <div class="brand-logo">M</div>
      <div><strong>Monitor</strong></div>
    </div>

    <a
      class="nav-item"
      :class="{ active: activeTab === 'traffic' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'traffic')"
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
      </svg>
      Network Traffic
    </a>

    <a
      class="nav-item"
      :class="{ active: activeTab === 'resources' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'resources')"
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <rect x="2" y="2" width="20" height="8" rx="2" />
        <rect x="2" y="14" width="20" height="8" rx="2" />
        <circle cx="6" cy="6" r="1" fill="currentColor" />
        <circle cx="6" cy="18" r="1" fill="currentColor" />
      </svg>
      Resources
    </a>

    <a
      class="nav-item"
      :class="{ active: activeTab === 'topips' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'topips')"
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="9" />
        <path d="M3 12h18M12 3a15 15 0 0 1 0 18a15 15 0 0 1 0-18" />
      </svg>
      Clients
    </a>

    <a
      class="nav-item"
      :class="{ active: activeTab === 'latency' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'latency')"
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="12" cy="12" r="9" />
        <path d="M12 7v5l3 2" />
      </svg>
      Latency
    </a>

    <a
      v-if="showRelay"
      class="nav-item"
      :class="{ active: activeTab === 'relay' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'relay')"
    >
      <!-- A waypoint: a route that passes through a node on its way. The rest of
           this list is built from one circle and a few strokes, so this is too —
           the old two-dots-and-an-arrowhead was the only glyph here wearing an
           arrow, and at 19px it read as a "next" button rather than a hop. -->
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M3 12h4" />
        <circle cx="12" cy="12" r="3" />
        <path d="M17 12h4" />
      </svg>
      Relay
    </a>

    <div class="mini-card">
      <strong>Sources {{ sourceCount }}</strong>
    </div>
  </aside>
</template>
