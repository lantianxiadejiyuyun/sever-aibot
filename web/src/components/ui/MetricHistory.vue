<script setup lang="ts">
import { computed, ref } from 'vue'
const props=defineProps<{title:string;unit:string;points:{time:string;value:number|null}[];maximum?:number;gapMs?:number}>()
const active=ref<number|null>(null)
const max=computed(()=>props.maximum ?? Math.max(1,...props.points.map(p=>p.value??0)))
const start=computed(()=>new Date(props.points[0]?.time??0).getTime())
const end=computed(()=>new Date(props.points.at(-1)?.time??0).getTime())
const x=(time:string)=>48+(new Date(time).getTime()-start.value)/Math.max(1,end.value-start.value)*660
const y=(v:number)=>172-v/max.value*148
const paths=computed(()=>{const paths:string[]=[];let part='';let last=0;for(const p of props.points){const t=new Date(p.time).getTime();if(p.value===null || (last && t-last>(props.gapMs??180000))){if(part)paths.push(part);part=''}if(p.value!==null)part+=`${part?' L':'M'}${x(p.time)},${y(p.value)}`;last=t}if(part)paths.push(part);return paths})
const isolated=computed(()=>props.points.filter((p,i,all)=>p.value!==null && (i===0 || all[i-1]!.value===null || Date.parse(p.time)-Date.parse(all[i-1]!.time)>(props.gapMs??180000)) && (i===all.length-1 || all[i+1]!.value===null || Date.parse(all[i+1]!.time)-Date.parse(p.time)>(props.gapMs??180000))))
function navigate(e:KeyboardEvent){if(!props.points.length)return;if(e.key==='ArrowRight'||e.key==='ArrowLeft'){e.preventDefault();active.value=Math.max(0,Math.min(props.points.length-1,(active.value??-1)+(e.key==='ArrowRight'?1:-1)))}}
const selected=computed(()=>active.value===null?null:props.points[active.value])
function hover(e:PointerEvent){const el=e.currentTarget as SVGSVGElement;const rect=el.getBoundingClientRect();const v=(e.clientX-rect.left)/rect.width*760;let nearest=-1,distance=Infinity;props.points.forEach((p,i)=>{const d=Math.abs(x(p.time)-v);if(d<distance){distance=d;nearest=i}});active.value=nearest<0?null:nearest}
const fmt=(n:number|null)=>n===null?'不可用':`${n.toFixed(1)} ${props.unit}`
</script>
<template>
<section class="metric-chart"><h3>{{title}}</h3>
<p v-if="!points.length" class="muted">此时间范围暂无采样</p>
<template v-else>
<svg viewBox="0 0 760 208" role="img" tabindex="0" :aria-label="title+'历史曲线，左右方向键查看采样值，详细值见下方数据表'" @keydown="navigate" @pointermove="hover" @pointerleave="active=null">
<g v-for="f in [0,0.5,1]" :key="f"><line x1="48" x2="708" :y1="y(max*f)" :y2="y(max*f)" class="grid"/><text x="40" :y="y(max*f)+4" text-anchor="end">{{(max*f).toFixed(max<10?1:0)}}</text></g>
<path v-for="(p,i) in paths" :key="i" :d="p" class="series"/>
<circle v-for="(p,i) in isolated" :key="'isolated-'+i" :cx="x(p.time)" :cy="y(p.value!)" r="4" class="dot"/>
<template v-if="selected"><line :x1="x(selected.time)" :x2="x(selected.time)" y1="24" y2="172" class="crosshair"/><circle v-if="selected.value!==null" :cx="x(selected.time)" :cy="y(selected.value)" r="4" class="dot"/></template>
<text x="48" y="198">{{new Date(points[0]!.time).toLocaleString()}}</text><text x="708" y="198" text-anchor="end">{{new Date(points.at(-1)!.time).toLocaleString()}}</text>
</svg>
<p class="readout" aria-live="polite">{{selected?new Date(selected.time).toLocaleString()+' · '+fmt(selected.value):'移动指针查看采样值；采样缺口不会连线。'}}</p>
<details><summary>查看数据表（{{points.length}} 条）</summary><div class="history-table"><table><thead><tr><th>时间</th><th>{{title}}（{{unit}}）</th></tr></thead><tbody><tr v-for="(p,i) in points" :key="i"><td>{{new Date(p.time).toLocaleString()}}</td><td>{{fmt(p.value)}}</td></tr></tbody></table></div></details>
</template></section>
</template>
<style scoped>
.metric-chart{--series:#2a78d6;--chart-surface:#fcfcfb;background:var(--chart-surface);color:var(--text);padding:16px;border-radius:8px;min-width:0}.metric-chart h3{font-size:14px;margin:0 0 12px}.metric-chart svg{width:100%;height:auto;display:block;touch-action:pan-y}.metric-chart text{font-size:16px;fill:currentColor;opacity:.7}.grid{stroke:currentColor;stroke-opacity:.12;stroke-width:1}.series{fill:none;stroke:var(--series);stroke-width:2;vector-effect:non-scaling-stroke}.dot{fill:var(--series);stroke:var(--chart-surface);stroke-width:2}.crosshair{stroke:currentColor;stroke-opacity:.5;stroke-width:1}.readout{font-size:12px;min-height:20px}.history-table{max-height:260px;overflow:auto}.history-table table{width:100%;font-size:12px}.history-table td,.history-table th{text-align:left;padding:6px}summary{cursor:pointer;font-size:12px}:global([data-theme=dark] .metric-chart){--series:#3987e5;--chart-surface:#1a1a19}@media(max-width:600px){.metric-chart text{font-size:22px}.metric-chart g text{font-size:18px}.dot{r:8px}}@media(forced-colors:active){.series{stroke:CanvasText}.dot{fill:CanvasText}.grid,.crosshair{stroke:CanvasText}}
</style>
