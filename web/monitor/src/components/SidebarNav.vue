<script setup lang="ts">
import type { Tab } from "../types";

defineProps<{
  activeTab: Tab;
  sourceCount: number;
  // How many of those nodes still have quota to serve with.
  onlineCount: number;
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
      <!-- A radar sweep: the mark of a thing that watches. -->
      <div class="brand-logo" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M20.5 8.5A9.5 9.5 0 1 1 15.5 3.3" />
          <path d="M16.2 10.2A5 5 0 1 1 13.8 7.3" />
          <circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" />
          <path d="M12 12l7-7" />
        </svg>
      </div>
      <div class="brand-text">
        <strong>singbox-deploy</strong>
        <span>Monitor</span>
      </div>
    </div>

    <nav class="nav" aria-label="Pages">
      <a
        class="nav-item"
        :class="{ active: activeTab === 'traffic' }"
        href="#"
        title="Network Traffic"
        @click.prevent="$emit('update:activeTab', 'traffic')"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M22 12h-4l-3 9L9 3l-3 9H2" />
        </svg>
        <span class="nav-label">Network Traffic</span>
      </a>

      <a
        class="nav-item"
        :class="{ active: activeTab === 'resources' }"
        href="#"
        title="Resources"
        @click.prevent="$emit('update:activeTab', 'resources')"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="2" y="2" width="20" height="8" rx="2" />
          <rect x="2" y="14" width="20" height="8" rx="2" />
          <circle cx="6" cy="6" r="1" fill="currentColor" />
          <circle cx="6" cy="18" r="1" fill="currentColor" />
        </svg>
        <span class="nav-label">Resources</span>
      </a>

      <a
        class="nav-item"
        :class="{ active: activeTab === 'topips' }"
        href="#"
        title="Clients"
        @click.prevent="$emit('update:activeTab', 'topips')"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="12" cy="12" r="9" />
          <path d="M3 12h18M12 3a15 15 0 0 1 0 18a15 15 0 0 1 0-18" />
        </svg>
        <span class="nav-label">Clients</span>
      </a>

      <a
        class="nav-item"
        :class="{ active: activeTab === 'latency' }"
        href="#"
        title="Latency"
        @click.prevent="$emit('update:activeTab', 'latency')"
      >
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 7v5l3 2" />
        </svg>
        <span class="nav-label">Latency</span>
      </a>

      <a
        v-if="showRelay"
        class="nav-item"
        :class="{ active: activeTab === 'relay' }"
        href="#"
        title="Relay"
        @click.prevent="$emit('update:activeTab', 'relay')"
      >
        <!-- A waypoint: a route that passes through a node on its way. -->
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M3 12h4" />
          <circle cx="12" cy="12" r="3" />
          <path d="M17 12h4" />
        </svg>
        <span class="nav-label">Relay</span>
      </a>
    </nav>

    <div class="fleet" :title="`${onlineCount} of ${sourceCount} nodes serving`">
      <div class="fleet-head"><span>Fleet</span></div>
      <div class="fleet-count">
        <strong>{{ sourceCount }}</strong>
        <span>node{{ sourceCount === 1 ? "" : "s" }}</span>
      </div>
      <div class="fleet-line">
        <span class="dot"></span>
        <em>{{ onlineCount }}</em> online
        <template v-if="sourceCount - onlineCount > 0">· {{ sourceCount - onlineCount }} limited</template>
      </div>
      <div class="fleet-line quiet">Polling every 10s</div>
    </div>
  </aside>
</template>
