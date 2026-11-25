<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import PageHeader from '@/components/ui/PageHeader.vue'
import MetricHistory from '@/components/ui/MetricHistory.vue'
import { request,errorMessage } from '@/api/client'
interface Metrics { sampleId?:string;rootDiskTotal:number|null;rootDiskUsed:number|null;rootDiskUsedPercent:number|null;collectedAt:string;cpuPercent:number|null;memUsed:number|null;memTotal:number|null;load1:number|null;networkRxBytes:Record<string,number>|null;networkTxBytes:Record<string,number>|null;diskReadBytes:Record<string,number>|null;diskWriteBytes:Record<string,number>|null;status:Record<string,string> }
interface Sample {deviceId:string;receivedAt:string;metrics:Metrics}
interface Device {deviceId:string;name:string;online:boolean;stale:boolean;latest:Sample|null}
const devices=ref<Device[]>([]),device=ref(''),hours=ref(24),samples=ref<Sample[]>([]),error=ref(''),busy=ref(false)
let sequence=0,timer:ReturnType<typeof setInterval>|undefined
const current=computed(()=>devices.value.find(d=>d.deviceId===device.value))
function sampleTime(s:Sample){return s.metrics.sampleId?s.metrics.collectedAt:s.receivedAt}
const disk=computed(()=>samples.value.map(s=>({time:sampleTime(s),value:s.metrics.rootDiskUsedPercent??null})))
const cpu=computed(()=>samples.value.map(s=>({time:sampleTime(s),value:s.metrics.cpuPercent})))
const memory=computed(()=>samples.value.map(s=>({time:sampleTime(s),value:s.metrics.memTotal&&s.metrics.memUsed!==null?s.metrics.memUsed/s.metrics.memTotal*100:null})))
const load=computed(()=>samples.value.map(s=>({time:sampleTime(s),value:s.metrics.load1})))
async function history(){const n=++sequence;samples.value=[];if(!device.value)return;busy.value=true;error.value='';try{const data=await request<{items:Sample[]}>(`/devices/${encodeURIComponent(device.value)}/metrics`,{query:{hours:hours.value}});if(n===sequence)samples.value=data.items.slice().reverse()}catch(e){if(n===sequence)error.value=errorMessage(e)}finally{if(n===sequence)busy.value=false}}
async function refresh(){try{devices.value=(await request<{items:Device[]}>('/metrics/overview')).items;if(!device.value&&devices.value[0])device.value=devices.value[0].deviceId;else await history()}catch(e){error.value=errorMessage(e)}}
const metricNames:Record<string,string>={cpu:'CPU',memory:'内存',load:'系统负载',network:'网络',disk:'磁盘 I/O',diskCapacity:'系统盘容量'}
const statusText:Record<string,string>={ok:'正常',warming_up:'等待第二次采样',unsupported:'当前系统不支持',unavailable:'采集不可用'}
watch([device,hours],history)
onMounted(()=>{void refresh();timer=setInterval(refresh,60000)})
onUnmounted(()=>{sequence++;if(timer)clearInterval(timer)})
</script>
<template>
<PageHeader eyebrow="MONITOR / METRICS" title="性能监控" />
<div class="stack">
<section class="panel"><div class="panel-body flex" style="gap:12px;flex-wrap:wrap">
<label>设备 <select v-model="device" class="input"><option v-for="d in devices" :key="d.deviceId" :value="d.deviceId">{{d.name}}</option></select></label>
<label>范围 <select v-model.number="hours" class="input"><option :value="1">最近 1 小时</option><option :value="6">最近 6 小时</option><option :value="24">最近 24 小时</option><option :value="168">最近 7 天</option></select></label>
<button class="btn" :disabled="busy" @click="refresh">{{busy?'加载中…':'刷新'}}</button>
<span v-if="current">{{current.online?'设备在线':'设备离线'}} · {{current.stale?'暂无新鲜采样':'采样正常'}}</span>
</div></section>
<p v-if="error" class="notice notice-danger" role="alert">{{error}}</p>
<p v-if="!devices.length&&!error" class="notice">暂无设备。添加并运行新版 Agent 后即可采集。</p>
<p class="muted">Agent 每分钟独立采样，每台设备保留最近 1440 条。离线样本在重连后补传，图表使用实际采集时间；内存缓存不跨 Agent 重启保留。范围内无样本或不支持的指标显示为空，不补零。</p>
<div v-if="current?.latest" class="flex" style="gap:12px;flex-wrap:wrap"><span v-for="(status,key) in current.latest.metrics.status" :key="key">{{metricNames[key]??key}}：{{statusText[status]??status}}</span></div>
<div class="metrics-grid"><MetricHistory title="CPU 使用率" unit="%" :points="cpu" :maximum="100"/><MetricHistory title="内存使用率" unit="%" :points="memory" :maximum="100"/></div>
<div class="metrics-grid"><MetricHistory title="1 分钟系统负载" unit="" :points="load"/><MetricHistory title="系统盘使用率" unit="%" :points="disk" :maximum="100"/></div>
<p v-if="current?.latest" class="muted">采集时间：{{new Date(sampleTime(current.latest)).toLocaleString()}} · 接收时间：{{new Date(current.latest.receivedAt).toLocaleString()}}</p>
<section v-if="current?.latest" class="panel"><div class="panel-head"><h2 class="panel-title">最近累计 I/O 计数</h2></div><div class="panel-body"><p class="muted">按接口/块设备分别报告；设备重启后计数会归零。磁盘父设备与分区可能重叠，请勿相加。</p><div class="detail-grid"><div><h3>网络（字节）</h3><div v-for="(v,k) in current.latest.metrics.networkRxBytes" :key="k">{{k}}：接收 {{v.toLocaleString()}} / 发送 {{current.latest.metrics.networkTxBytes?.[k]?.toLocaleString()??'不可用'}}</div></div><div><h3>磁盘 I/O（字节）</h3><div v-for="(v,k) in current.latest.metrics.diskReadBytes" :key="k">{{k}}：读 {{v.toLocaleString()}} / 写 {{current.latest.metrics.diskWriteBytes?.[k]?.toLocaleString()??'不可用'}}</div></div></div></div></section>
</div>
</template>
<style scoped>
.metrics-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}
@media(max-width:800px){.metrics-grid{grid-template-columns:minmax(0,1fr)}}
</style>
