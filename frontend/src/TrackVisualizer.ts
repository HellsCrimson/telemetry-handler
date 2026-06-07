// Canvas track visualiser ported from webui/static/app.js. It draws the car's
// path from position telemetry with pan (drag) and zoom (scroll).

import type { HistorySample } from "./telemetry";

interface PositionPoint {
  x: number;
  z: number;
  yaw: number;
  pitch: number;
  roll: number;
  speed: number;
}

export class TrackVisualizer {
  private canvas: HTMLCanvasElement;
  private ctx: CanvasRenderingContext2D;
  private onInfo: (text: string) => void;
  private positionData: PositionPoint[] = [];
  private currentIndex = -1;
  private zoom = 1;
  private panX = 0;
  private panY = 0;
  private isDragging = false;
  private dragStartX = 0;
  private dragStartY = 0;
  private trackMinX = 0;
  private trackMinZ = 0;
  private trackMaxX = 100;
  private trackMaxZ = 100;
  private carSize = 20;
  private padding = 40;
  private dpiScale = 1;
  private resizeListener: () => void;

  constructor(canvas: HTMLCanvasElement, onInfo: (text: string) => void) {
    this.canvas = canvas;
    this.ctx = canvas.getContext("2d") as CanvasRenderingContext2D;
    this.onInfo = onInfo;

    this.canvas.addEventListener("wheel", this.handleZoom);
    this.canvas.addEventListener("mousedown", this.handleDragStart);
    this.canvas.addEventListener("mousemove", this.handleDrag);
    this.canvas.addEventListener("mouseup", this.handleDragEnd);
    this.canvas.addEventListener("mouseleave", this.handleDragEnd);

    this.dpiScale = window.devicePixelRatio || 1;
    this.resizeListener = () => this.resizeCanvas();
    window.addEventListener("resize", this.resizeListener);
    this.resizeCanvas();
  }

  destroy() {
    this.canvas.removeEventListener("wheel", this.handleZoom);
    this.canvas.removeEventListener("mousedown", this.handleDragStart);
    this.canvas.removeEventListener("mousemove", this.handleDrag);
    this.canvas.removeEventListener("mouseup", this.handleDragEnd);
    this.canvas.removeEventListener("mouseleave", this.handleDragEnd);
    window.removeEventListener("resize", this.resizeListener);
  }

  resizeCanvas() {
    const rect = this.canvas.getBoundingClientRect();
    this.canvas.width = rect.width * this.dpiScale;
    this.canvas.height = rect.height * this.dpiScale;
    this.ctx.scale(this.dpiScale, this.dpiScale);
    this.render();
  }

  private handleZoom = (e: WheelEvent) => {
    e.preventDefault();
    const delta = e.deltaY > 0 ? 0.9 : 1.1;
    this.zoom *= delta;
    this.zoom = Math.max(0.1, Math.min(10, this.zoom));
    this.render();
  };

  private handleDragStart = (e: MouseEvent) => {
    this.isDragging = true;
    this.dragStartX = e.clientX;
    this.dragStartY = e.clientY;
  };

  private handleDrag = (e: MouseEvent) => {
    if (!this.isDragging) return;
    this.panX += e.clientX - this.dragStartX;
    this.panY += e.clientY - this.dragStartY;
    this.dragStartX = e.clientX;
    this.dragStartY = e.clientY;
    this.render();
  };

  private handleDragEnd = () => {
    this.isDragging = false;
  };

  resetView() {
    this.zoom = 1;
    this.panX = 0;
    this.panY = 0;
    this.render();
  }

  updateData(history: HistorySample[]) {
    this.positionData = history.map((sample) => ({
      x: Number(sample.telemetry.PositionX),
      z: Number(sample.telemetry.PositionZ),
      yaw: Number(sample.telemetry.Yaw),
      pitch: Number(sample.telemetry.Pitch),
      roll: Number(sample.telemetry.Roll),
      speed: Number(sample.telemetry.Speed),
    }));
    if (this.positionData.length > 0) {
      this.computeBounds();
    }
  }

  setCurrentIndex(index: number) {
    this.currentIndex = Math.min(index, this.positionData.length - 1);
    this.render();
  }

  private computeBounds() {
    let minX = Infinity, maxX = -Infinity;
    let minZ = Infinity, maxZ = -Infinity;
    this.positionData.forEach((pos) => {
      minX = Math.min(minX, pos.x);
      maxX = Math.max(maxX, pos.x);
      minZ = Math.min(minZ, pos.z);
      maxZ = Math.max(maxZ, pos.z);
    });
    this.trackMinX = minX;
    this.trackMaxX = maxX;
    this.trackMinZ = minZ;
    this.trackMaxZ = maxZ;
    const rangeX = maxX - minX || 100;
    const rangeZ = maxZ - minZ || 100;
    const padding = Math.max(rangeX, rangeZ) * 0.1;
    this.trackMinX -= padding;
    this.trackMinZ -= padding;
    this.trackMaxX += padding;
    this.trackMaxZ += padding;
  }

  private worldToCanvas(x: number, z: number) {
    const trackWidth = this.trackMaxX - this.trackMinX;
    const trackHeight = this.trackMaxZ - this.trackMinZ;
    const canvasWidth = this.canvas.width / this.dpiScale - this.padding * 2;
    const canvasHeight = this.canvas.height / this.dpiScale - this.padding * 2;
    const scale = Math.min(canvasWidth / trackWidth, canvasHeight / trackHeight) * this.zoom;
    const centerX = this.canvas.width / this.dpiScale / 2;
    const centerY = this.canvas.height / this.dpiScale / 2;
    const trackCenterX = (this.trackMinX + this.trackMaxX) / 2;
    const trackCenterZ = (this.trackMinZ + this.trackMaxZ) / 2;
    return {
      canvasX: centerX + (x - trackCenterX) * scale + this.panX,
      canvasY: centerY + (z - trackCenterZ) * scale + this.panY,
    };
  }

  private drawTrackPath() {
    if (this.positionData.length < 2) return;
    this.ctx.strokeStyle = "#42d477";
    this.ctx.lineWidth = 2;
    this.ctx.lineCap = "round";
    this.ctx.lineJoin = "round";
    this.ctx.beginPath();
    const start = this.worldToCanvas(this.positionData[0].x, this.positionData[0].z);
    this.ctx.moveTo(start.canvasX, start.canvasY);
    for (let i = 1; i < this.positionData.length; i++) {
      const pos = this.worldToCanvas(this.positionData[i].x, this.positionData[i].z);
      this.ctx.lineTo(pos.canvasX, pos.canvasY);
    }
    this.ctx.stroke();
  }

  private drawCar(index: number) {
    if (index < 0 || index >= this.positionData.length) return;
    const data = this.positionData[index];
    const pos = this.worldToCanvas(data.x, data.z);
    this.ctx.save();
    this.ctx.translate(pos.canvasX, pos.canvasY);
    this.ctx.rotate((data.yaw * Math.PI) / 180);
    const width = this.carSize * 0.8;
    const height = this.carSize;
    const rollInfluence = Math.max(-1, Math.min(1, data.roll / 45));
    const pitchInfluence = Math.max(-1, Math.min(1, data.pitch / 45));
    const hue = 120 + rollInfluence * 30;
    const saturation = 80 + pitchInfluence * 20;
    this.ctx.fillStyle = `hsl(${hue}, ${saturation}%, 45%)`;
    this.ctx.fillRect(-width / 2, -height / 2, width, height);
    this.ctx.strokeStyle = "#ffffff";
    this.ctx.lineWidth = 1;
    this.ctx.strokeRect(-width / 2, -height / 2, width, height);
    this.ctx.fillStyle = "#ffffff";
    this.ctx.fillRect(-width / 2 + 2, -height / 2 + 2, width - 4, height / 3 - 2);
    this.ctx.restore();
  }

  private drawGrid() {
    const trackWidth = this.trackMaxX - this.trackMinX;
    const trackHeight = this.trackMaxZ - this.trackMinZ;
    const canvasWidth = this.canvas.width / this.dpiScale - this.padding * 2;
    const canvasHeight = this.canvas.height / this.dpiScale - this.padding * 2;
    const scale = Math.min(canvasWidth / trackWidth, canvasHeight / trackHeight) * this.zoom;
    this.ctx.strokeStyle = "rgba(212, 218, 227, 0.1)";
    this.ctx.lineWidth = 1;
    this.ctx.font = "11px Inter, sans-serif";
    this.ctx.fillStyle = "rgba(169, 176, 183, 0.5)";
    const gridStep = this.getGridStep(scale);
    const startX = Math.floor(this.trackMinX / gridStep) * gridStep;
    const startZ = Math.floor(this.trackMinZ / gridStep) * gridStep;
    for (let x = startX; x <= this.trackMaxX; x += gridStep) {
      const pos = this.worldToCanvas(x, this.trackMinZ);
      this.ctx.beginPath();
      this.ctx.moveTo(pos.canvasX, this.padding);
      this.ctx.lineTo(pos.canvasX, this.canvas.height / this.dpiScale - this.padding);
      this.ctx.stroke();
    }
    for (let z = startZ; z <= this.trackMaxZ; z += gridStep) {
      const pos = this.worldToCanvas(this.trackMinX, z);
      this.ctx.beginPath();
      this.ctx.moveTo(this.padding, pos.canvasY);
      this.ctx.lineTo(this.canvas.width / this.dpiScale - this.padding, pos.canvasY);
      this.ctx.stroke();
    }
  }

  private getGridStep(scale: number) {
    const pixelStep = 50;
    const worldStep = pixelStep / scale;
    const magnitude = Math.floor(Math.log10(worldStep));
    const mantissa = Math.ceil(worldStep / Math.pow(10, magnitude));
    return mantissa * Math.pow(10, magnitude);
  }

  render() {
    if (!this.ctx) return;
    const width = this.canvas.width / this.dpiScale;
    const height = this.canvas.height / this.dpiScale;
    this.ctx.fillStyle = "#080a0c";
    this.ctx.fillRect(0, 0, width, height);
    this.ctx.strokeStyle = "#343a40";
    this.ctx.lineWidth = 1;
    this.ctx.strokeRect(this.padding, this.padding, width - this.padding * 2, height - this.padding * 2);
    this.ctx.save();
    this.ctx.beginPath();
    this.ctx.rect(this.padding, this.padding, width - this.padding * 2, height - this.padding * 2);
    this.ctx.clip();
    this.drawGrid();
    this.drawTrackPath();
    if (this.currentIndex >= 0) {
      this.drawCar(this.currentIndex);
    }
    this.ctx.restore();
    const zoomText = `${(this.zoom * 100).toFixed(0)}%`;
    this.onInfo(`Samples: ${this.positionData.length} | Zoom: ${zoomText}`);
  }
}
