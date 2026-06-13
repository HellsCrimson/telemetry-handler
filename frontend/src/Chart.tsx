import { useEffect, useRef } from "react";
import * as echarts from "echarts";
import { type ChartDefinition, type HistorySample, colorForField } from "./telemetry";

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
}

// Chart renders a single ECharts line panel from the rolling telemetry history,
// matching the option set used by the original dashboard.
export default function Chart({ definition, history, xLabels, highlights }: ChartProps) {
  const elRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!elRef.current) return;
    const chart = echarts.init(elRef.current, "dark", { renderer: "canvas" });
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
      lineStyle: { width: 2, color: colorForField(field) },
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

    chart.setOption({
      backgroundColor: "transparent",
      title: { text: definition.title, left: 10, top: 8, textStyle: { fontSize: 13 } },
      tooltip: { trigger: "axis" },
      legend: { top: 34, type: "scroll" },
      grid: { left: 46, right: 18, top: 74, bottom: 34 },
      xAxis: { type: "category", boundaryGap: false, data: labels },
      yAxis: { type: "value", min: definition.yMin, max: yMax, scale: definition.yMin === undefined && yMax === undefined },
      series,
      animation: false,
    });
  }, [definition, history, xLabels, highlights]);

  return <div className="chart" ref={elRef} />;
}
