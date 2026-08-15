<script setup lang="ts">
import type { Tab } from "../types";

defineProps<{
  activeTab: Tab;
  sourceCount: number;
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
      class="nav-item"
      :class="{ active: activeTab === 'relay' }"
      href="#"
      @click.prevent="$emit('update:activeTab', 'relay')"
    >
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <circle cx="5" cy="12" r="2.5" />
        <circle cx="19" cy="12" r="2.5" />
        <path d="M7.5 12h9" />
        <path d="M14 9l3 3-3 3" />
      </svg>
      Relay
    </a>

    <div class="mini-card">
      <strong>Sources {{ sourceCount }}</strong>
    </div>
  </aside>
</template>
