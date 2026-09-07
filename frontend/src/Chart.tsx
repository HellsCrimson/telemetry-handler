import { useEffect, useRef } from "react";
import * as echarts from "echarts";
import { type ChartDefinition, type HistorySample, colorForField } from "./telemetry";
import { chartAxis, chartBase } from "./design/chartTheme";

// Highlight shades an x-range of the chart (by sample index) — used by the
// Review tab to mark where the analysis found an issue.
export interface Highlight {
  fromIndex: number;
  toIndex: number;
  color: string;
  name?: string;
}

interface ChartProps {
  definition: ChartDefinition;
  history: HistorySample[];
  // xLabels overrides the default wall-clock x-axis labels (the Review tab uses
  // relative lap time instead).
  xLabels?: string[];
  highlights?: Highlight[];
  // footer is the unit/range line under the plot; omitted when there is nothing
  // useful to say.
  footer?: string;
  // height overrides the plot height for panels that need more or less room.
  height?: number;
}

// Chart renders a single ECharts line panel from the rolling telemetry history,
// matching the option set used by the original dashboard.
export default function Chart({ definition, history, xLabels, highlights, footer, height }: ChartProps) {
  const elRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!elRef.current) return;
    // No built-in theme: everything the plot draws comes from design/chartTheme.
    const chart = echarts.init(elRef.current, undefined, { renderer: "canvas" });
    chartRef.current = chart;
    const onResize = () => chart.resize();
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      chart.dispose();
      chartRef.current = null;
    };
  }, []);

  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const labels = xLabels ?? history.map((sample) => new Date(sample.time).toLocaleTimeString());
    const series: Record<string, unknown>[] = definition.fields.map((field) => ({
      name: field.name,
      type: "line" as const,
      showSymbol: false,
      smooth: true,
      sampling: "lttb",
      lineStyle: { width: 1.4, color: colorForField(field) },
      itemStyle: { color: colorForField(field) },
      data: history.map((sample) => {
        const value = Number(sample.telemetry[field.field] ?? 0);
        return field.transform ? field.transform(value) : value;
      }),
    }));

    // markArea lives on a series; attach the highlight zones to the first one.
    if (highlights && highlights.length > 0 && series.length > 0) {
      series[0].markArea = {
        silent: true,
        label: { show: false },
        data: highlights.map((h) => [
          { xAxis: h.fromIndex, itemStyle: { color: h.color }, name: h.name },
          { xAxis: h.toIndex },
        ]),
      };
    }

    // Scale the axis to a telemetry field's peak (e.g. engine max RPM) instead
    // of plotting that value as its own line.
    let yMax: number | undefined;
    if (definition.yMaxField) {
      let peak = 0;
      for (const sample of history) {
        const v = Number(sample.telemetry[definition.yMaxField] ?? 0);
        if (v > peak) peak = v;
      }
      if (peak > 0) yMax = peak + (definition.yMaxPad ?? 0);
    }

    // The frame draws the title and legend, so the plot carries neither — that
    // is what keeps the chart aligned with the panels around it instead of
    // floating inside its own padding.
    chart.setOption({
      ...chartBase,
      xAxis: { ...chartAxis, type: "category", boundaryGap: false, data: labels, splitLine: { show: false } },
      yAxis: {
        ...chartAxis,
        type: "value",
        min: definition.yMin,
        max: yMax,
        scale: definition.yMin === undefined && yMax === undefined,
      },
      series,
      animation: false,
    });
  }, [definition, history, xLabels, highlights]);

  return (
    <div className="chart-frame">
      <div className="panel-head">
        <div className="panel-label">{definition.title.toUpperCase()}</div>
        <div className="spacer" />
        <div className="chart-legend">
          {definition.fields.map((field) => (
            <span key={field.field} style={{ color: colorForField(field) }}>
              <i />
              <em style={{ fontStyle: "normal", color: "var(--ink-lo)" }}>{field.name}</em>
            </span>
          ))}
        </div>
      </div>
      <div className="chart-well">
        <div className="chart" ref={elRef} style={{ height: height ?? 200 }} />
      </div>
      {footer && <div className="chart-foot">{footer}</div>}
    </div>
  );
}
