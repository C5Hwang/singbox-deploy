// The one latency ramp, in the two grades a page needs it: fills for the matrix
// cells and inks for latency printed as text.
//
// They are not the same colours because they are not on the same background. A
// fill is read against white and can be as bright as it likes; text is read on
// white, and #4ade80 on white is 1.6:1 — a green that is unreadable is not a
// green. The text grade is the same four hues taken several steps darker, so
// the two agree about which bucket a number is in while each stays legible
// where it lives.
export interface LatencyStep {
  limit: number;
  fill: string;
  ink: string;
  text: string;
}

export const LATENCY_STEPS: LatencyStep[] = [
  { limit: 150, fill: "#4ade80", ink: "#0b3a1e", text: "#15803d" },
  { limit: 250, fill: "#fbbf24", ink: "#3d2a00", text: "#a16207" },
  { limit: 350, fill: "#fb7a3c", ink: "#4a1a02", text: "#c2410c" },
  { limit: Infinity, fill: "#f2544f", ink: "#330807", text: "#b91c1c" },
];

// A probe that answered nothing is not "slow", it is out — off the end of the
// ramp rather than at the far end of it, so it takes the one solid fill.
export const LATENCY_DEAD = { fill: "#dc2626", ink: "#ffffff", text: "#b91c1c" };
export const LATENCY_MISSING = { fill: "#f1f4f9", ink: "#98a2b3", text: "#98a2b3" };

// Loss is a state, not a magnitude, so it takes reserved colours rather than a
// step of the latency ramp.
export const LOSS_WARNING = "#b45309";
export const LOSS_CRITICAL = "#7f1d1d";
// No loss is the quiet case and reads as one: the figure is still printed, in
// the ink the rest of the dashboard uses for a number that needs no attention.
export const LOSS_CLEAR = "#5b6b84";

export function latencyStep(ms: number | null): LatencyStep | typeof LATENCY_DEAD | typeof LATENCY_MISSING {
  if (ms === null) return LATENCY_DEAD;
  return LATENCY_STEPS.find((s) => ms < s.limit)!;
}
