// The one latency ramp, in the two grades a page needs it: fills for the matrix
// cells and inks for latency printed as text.
//
// They are not the same colours because they are not on the same background. A
// fill is read against the card and can be as bright as it likes; text is read
// on the card, and a bright green on white is unreadable. The text grade is the
// same four hues taken several steps darker — or lighter, on the dark theme —
// so the two agree about which bucket a number is in while each stays legible
// where it lives.
//
// The colours themselves are the stylesheet's, reached through its variables:
// every one of these is set on an element, so the theme in force is what
// resolves it, and the ramp turns with the theme like the rest of the page.
export interface LatencyStep {
  limit: number;
  fill: string;
  ink: string;
  text: string;
}

export const LATENCY_STEPS: LatencyStep[] = [
  { limit: 150, fill: "var(--lat-1-fill)", ink: "var(--lat-1-ink)", text: "var(--lat-1-text)" },
  { limit: 250, fill: "var(--lat-2-fill)", ink: "var(--lat-2-ink)", text: "var(--lat-2-text)" },
  { limit: 350, fill: "var(--lat-3-fill)", ink: "var(--lat-3-ink)", text: "var(--lat-3-text)" },
  { limit: Infinity, fill: "var(--lat-4-fill)", ink: "var(--lat-4-ink)", text: "var(--lat-4-text)" },
];

// A probe that answered nothing is not "slow", it is out — off the end of the
// ramp rather than at the far end of it, so it takes the one solid fill.
export const LATENCY_DEAD = { fill: "var(--lat-dead-fill)", ink: "var(--lat-dead-ink)", text: "var(--lat-dead-text)" };
export const LATENCY_MISSING = { fill: "var(--lat-none-fill)", ink: "var(--lat-none-ink)", text: "var(--lat-none-text)" };

// Loss is a state, not a magnitude, so it takes reserved colours rather than a
// step of the latency ramp.
export const LOSS_WARNING = "var(--loss-warning)";
export const LOSS_CRITICAL = "var(--loss-critical)";
// No loss is the quiet case and reads as one: the figure is still printed, in
// the ink the rest of the dashboard uses for a number that needs no attention.
export const LOSS_CLEAR = "var(--loss-clear)";

export function latencyStep(ms: number | null): LatencyStep | typeof LATENCY_DEAD | typeof LATENCY_MISSING {
  if (ms === null) return LATENCY_DEAD;
  return LATENCY_STEPS.find((s) => ms < s.limit)!;
}
