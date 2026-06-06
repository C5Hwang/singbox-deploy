<script setup lang="ts">
defineProps<{
  activeTab: "traffic" | "resources";
  sourceCount: number;
}>();
defineEmits<{
  "update:activeTab": [value: "traffic" | "resources"];
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

    <div class="mini-card">
      <strong>Sources {{ sourceCount }}</strong>
    </div>
  </aside>
</template>
