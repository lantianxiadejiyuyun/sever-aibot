<script setup lang="ts">
const days = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']
// Static, clearly-labelled sample shape. The real traffic endpoint does not
// exist yet, so this is never presented as live telemetry.
const values = [22, 34, 29, 46, 41, 58, 52, 66, 61, 74, 69, 78, 63, 71, 56, 62, 49, 53, 44, 48]
const width = 720
const height = 190
const padding = 8
const max = Math.max(...values)
const step = (width - padding * 2) / (values.length - 1)
const points = values
  .map((value, index) => {
    const x = padding + index * step
    const y = height - padding - (value / max) * (height - padding * 2)
    return `${x.toFixed(1)},${y.toFixed(1)}`
  })
  .join(' ')
const areaPoints = `${padding},${height - padding} ${points} ${width - padding},${height - padding}`
const gridRows = [0.25, 0.5, 0.75]
</script>

<template>
  <div class="chart-wrap">
    <svg
      class="traffic-chart"
      :viewBox="`0 0 ${width} ${height}`"
      preserveAspectRatio="none"
      role="img"
      aria-label="示例流量趋势图，非实时数据"
    >
      <defs>
        <linearGradient id="trafficFill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" style="stop-color: var(--primary); stop-opacity: 0.2" />
          <stop offset="100%" style="stop-color: var(--primary); stop-opacity: 0" />
        </linearGradient>
      </defs>
      <line
        v-for="row in gridRows"
        :key="row"
        class="chart-grid-line"
        :x1="padding"
        :x2="width - padding"
        :y1="height * row"
        :y2="height * row"
      />
      <polygon class="chart-area" :points="areaPoints" />
      <polyline class="chart-line" :points="points" />
    </svg>
    <div class="chart-axis">
      <span v-for="day in days" :key="day">{{ day }}</span>
    </div>
  </div>
</template>
