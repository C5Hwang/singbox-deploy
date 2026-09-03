import { ref } from "vue";

// One clock for every "12s ago" and "reset in 3d" on the page. A single ticking
// ref means every relative time on screen moves in step, and a card that reads
// it re-renders once a second rather than running a timer of its own.
export const now = ref(Date.now());
window.setInterval(() => {
  now.value = Date.now();
}, 1000);

// How long ago a stamp was, in the coarsest unit that still says something.
// The stamp is what the node reported, so a clock skewed the other way reads
// as "just now" rather than as a negative age.
export function formatAgo(value: string | number | Date): string {
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "NA";
  const seconds = Math.max(0, Math.round((now.value - then) / 1000));
  if (seconds < 5) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// How long until a stamp, for a quota cycle that has not reset yet.
export function formatUntil(value: string | number | Date): string {
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "NA";
  const seconds = Math.round((then - now.value) / 1000);
  if (seconds <= 0) return "now";
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  if (days > 0) return hours > 0 ? `${days}d ${hours}h` : `${days}d`;
  const minutes = Math.floor((seconds % 3600) / 60);
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${Math.max(1, minutes)}m`;
}
