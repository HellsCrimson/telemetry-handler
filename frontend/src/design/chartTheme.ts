// ECharts styling derived from the same tokens as the rest of the interface.
//
// Charts are the usual giveaway that a redesign was skin-deep: the panels change
// and the plots keep their library defaults, so the axes are the wrong grey and
// the tooltip is the wrong shape. Everything a chart draws comes from here.

/** Palette, mirroring design/tokens.css. Kept as literals because ECharts needs
 *  resolved colours rather than CSS custom properties. */
export const chartInk = {
  hi: "#e8ebef",
  ink: "#b9c0ca",
  mid: "#7c8593",
  lo: "#565e6b",
  faint: "#3f4753",
  line: "#232830",
  lineSoft: "#171b21",
  lineStrong: "#2e343e",
  inset: "#0e1116",
} as const;

/** Semantic series colours. Same six hues, same meanings as the UI. */
export const chartSem = {
  cold: "oklch(0.72 0.13 240)",
  cool: "oklch(0.78 0.11 205)",
  good: "oklch(0.78 0.15 152)",
  warn: "oklch(0.83 0.14 82)",
  crit: "oklch(0.68 0.18 25)",
  best: "oklch(0.72 0.15 305)",
} as const;

const AXIS_FONT = { fontFamily: "IBM Plex Mono", fontSize: 9.5 };

/** Axis styling shared by both axes. */
export const chartAxis = {
  axisLine: { lineStyle: { color: chartInk.line } },
  axisTick: { show: false },
  splitLine: { lineStyle: { color: chartInk.lineSoft } },
  axisLabel: { color: chartInk.lo, ...AXIS_FONT },
} as const;

/** Options every chart starts from: transparent ground (the frame supplies the
 *  surface), mono type, and a tooltip that matches the panel chrome. */
export const chartBase = {
  backgroundColor: "transparent",
  animationDuration: 600,
  textStyle: { fontFamily: "IBM Plex Mono" },
  tooltip: {
    trigger: "axis" as const,
    backgroundColor: chartInk.inset,
    borderColor: chartInk.lineStrong,
    borderWidth: 1,
    padding: [6, 9],
    textStyle: { color: chartInk.ink, fontFamily: "IBM Plex Mono", fontSize: 11 },
    axisPointer: { lineStyle: { color: chartInk.faint } },
  },
  // The frame draws the title and legend, so the plot itself only needs room
  // for its axes.
  grid: { left: 40, right: 12, top: 10, bottom: 24, containLabel: false },
} as const;

/** Series order for charts with no semantic meaning per line. Ink first: the
 *  primary trace should read as the subject, with comparisons receding. */
export const chartSeriesColors = [
  chartInk.hi,
  chartSem.cold,
  chartSem.good,
  chartSem.warn,
  chartSem.cool,
  chartSem.best,
  chartInk.mid,
  chartInk.faint,
];

/** line builds a series in the house style: no symbols, thin, no emphasis
 *  flicker on hover (a 60 Hz feed redraws often enough already). */
export function line(name: string, data: unknown[], color: string, width = 1.4) {
  return {
    name,
    type: "line" as const,
    data,
    showSymbol: false,
    smooth: false,
    lineStyle: { color, width },
    itemStyle: { color },
    emphasis: { disabled: true },
  };
}
