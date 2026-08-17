import { computed, ref, toValue, type MaybeRefOrGetter } from "vue";
import { fetchLatencySeries } from "./api";
import { buildTrendOption, lineSeries, msAxis, SOURCE_COLORS } from "./chartOptions";
import type { PingSeries, PingTarget } from "./types";

// The week of one-minute rounds one node recorded, drawn one line per probe.
//
// The latency page and the relay page ask this of the same endpoint and draw
// the same chart from it; all that differs is which probes they keep and what
// they call them. That is what this takes as arguments, and everything else —
// which part of the week is real, how it is labelled, how a round becomes a
// point — is the same measurement and lives here once.
export function usePingTrend(
  nodeKey: string,
  targets: MaybeRefOrGetter<PingTarget[]>,
  seriesName: (target: PingTarget) => string,
) {
  const history = ref<PingSeries | null>(null);
  const error = ref("");

  // The card that opened the modal carries only the newest round, so the week
  // is fetched here — once, on open, rather than on the page's minute poll.
  async function load() {
    try {
      history.value = await fetchLatencySeries(nodeKey);
    } catch (e) {
      error.value = `Latency history is unavailable: ${e instanceof Error ? e.message : String(e)}.`;
    }
  }

  // The grid the node sends is always a full week, one slot a minute, whether
  // or not anything was recorded in each — that is what makes it a grid. The
  // chart only draws the part of it that was: a node installed yesterday would
  // otherwise get an axis six days of which are empty, which reads as a broken
  // chart rather than as a young one.
  //
  // A round that answered nothing still counts as recorded, so an outage is
  // inside the window as a gap rather than trimmed off the end of it. loss is
  // what says a round happened; ms is null for the ones that answered nothing.
  const recorded = computed<[number, number] | null>(() => {
    const series = history.value;
    if (!series) return null;
    let first = -1;
    let last = -1;
    for (const target of toValue(targets)) {
      const track = series.series[target.id];
      if (!track) continue;
      for (let i = 0; i < track.loss.length; i++) {
        if (track.loss[i] < 0) continue;
        if (first < 0 || i < first) first = i;
        if (i > last) last = i;
      }
    }
    return first < 0 ? null : [first, last];
  });

  // What the subtitle claims has to be what the axis shows, so it reports the
  // span that was actually recorded rather than the week that was asked for.
  const spanLabel = computed(() => {
    const series = history.value;
    const span = recorded.value;
    if (!series || !span) return "no rounds yet";
    const hours = ((span[1] - span[0]) * series.step) / 3600;
    if (hours < 1) return "under an hour";
    if (hours < 48) return `last ${Math.round(hours)} h`;
    return `last ${Math.round(hours / 24)} days`;
  });

  // Every round the node recorded, at the minute it recorded it. A slot with no
  // value — a round that answered nothing, or a minute the monitor was not
  // running — becomes a null, which draws as a gap rather than as zero latency.
  function points(targetId: string): [number, number | null][] {
    const series = history.value;
    const track = series?.series[targetId];
    const span = recorded.value;
    if (!series || !track || !span) return [];
    const slots: [number, number | null][] = [];
    for (let i = span[0]; i <= span[1]; i++) {
      slots.push([(series.start + i * series.step) * 1000, track.ms[i]]);
    }
    return slots;
  }

  function ms(value: unknown): string {
    const n = Number(value);
    return Number.isFinite(n) ? `${n.toFixed(1)} ms` : "NA";
  }

  function buildOption(el: HTMLElement | undefined, showPeakAverage: boolean): Record<string, any> {
    const shown = toValue(targets);
    return buildTrendOption({
      el,
      unit: "minute",
      tooltipUnit: "minute",
      legend: shown.map(seriesName),
      // Nine probes at once, and the question is which of them is slowest, not
      // which of them was declared first.
      sortTooltip: true,
      tooltipValue: (p) => ms(Array.isArray(p.value) ? p.value[1] : p.value),
      yAxis: msAxis,
      series: () =>
        shown.map((target, i) =>
          lineSeries(seriesName(target), SOURCE_COLORS[i % SOURCE_COLORS.length], points(target.id), { dense: true }),
        ),
      peakAverage: { show: showPeakAverage, format: (v) => `${v.toFixed(0)} ms` },
    });
  }

  return { error, load, spanLabel, buildOption };
}
