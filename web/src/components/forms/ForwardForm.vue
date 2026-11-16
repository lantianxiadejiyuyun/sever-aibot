<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { Loader2, Save } from 'lucide-vue-next'
import FormField from '@/components/ui/FormField.vue'
import type { Device, Forward, Node } from '@/api/types'
import type { ForwardInput } from '@/api/endpoints'
import { forwardKindOptions, kindLabel, nodeSupportsKind, optionLabel } from '@/utils/node'

const props = withDefaults(
  defineProps<{
    initial?: Forward | null
    devices?: Device[]
    nodes?: Node[]
    defaultKind?: string
    submitting?: boolean
    serverError?: string
    submitLabel?: string
  }>(),
  {
    initial: null,
    devices: () => [],
    nodes: () => [],
    defaultKind: 'port',
    submitting: false,
    serverError: '',
    submitLabel: '保存转发',
  },
)

const emit = defineEmits<{
  submit: [payload: ForwardInput]
  cancel: []
}>()

const form = reactive<ForwardInput>({
  name: '',
  kind: 'port',
  deviceId: '',
  nodeId: '',
  clientId: '',
  targetIp: '',
  targetPort: 0,
  remotePort: 0,
  domain: '',
  tags: '',
  options: '{}',
})

const localError = ref('')

watch(
  () => props.initial,
  (forward) => {
    form.name = forward?.name ?? ''
    form.kind = forward?.kind || props.defaultKind
    form.deviceId = forward?.deviceId ?? ''
    form.nodeId = forward?.nodeId ?? ''
    form.clientId = forward?.clientId ?? ''
    form.targetIp = forward?.targetIp ?? ''
    form.targetPort = forward?.targetPort ?? 0
    form.remotePort = forward?.remotePort ?? 0
    form.domain = forward?.domain ?? ''
    form.tags = forward?.tags ?? ''
    form.options = forward?.options?.trim() ? forward.options : '{}'
  },
  { immediate: true },
)

watch(
  () => props.defaultKind,
  (kind) => {
    if (!props.initial) form.kind = kind
  },
)

const selectedNode = computed(() => props.nodes.find((node) => node.id === form.nodeId) || null)
const nodeSupport = computed(() => nodeSupportsKind(selectedNode.value, form.kind))
const needsPortPair = computed(() => form.kind === 'port')
const needsDomain = computed(() => form.kind === 'host')

function numberValue(value: number | string): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function submit(): void {
  localError.value = ''
  if (!form.name.trim()) {
    localError.value = '转发名称不能为空。'
    return
  }
  if (!form.nodeId) {
    localError.value = '请选择承载转发的节点。'
    return
  }
  if (!nodeSupport.value) {
    localError.value = `节点「${selectedNode.value?.name || form.nodeId}」未启用 ${kindLabel(form.kind)} 能力。`
    return
  }
  const targetPort = numberValue(form.targetPort)
  const remotePort = numberValue(form.remotePort)
  if (targetPort < 0 || targetPort > 65535 || remotePort < 0 || remotePort > 65535) {
    localError.value = '端口必须在 0-65535 之间。'
    return
  }
  if (needsPortPair.value && (targetPort === 0 || remotePort === 0)) {
    localError.value = '端口类转发必须填写入口端口和目标端口。'
    return
  }
  if (needsPortPair.value && !form.targetIp.trim()) {
    localError.value = '端口类转发必须填写目标地址。'
    return
  }
  if (needsDomain.value && !form.domain.trim()) {
    localError.value = '域名类转发必须填写域名。'
    return
  }
  const rawOptions = form.options.trim()
  if (rawOptions) {
    try {
      const parsed = JSON.parse(rawOptions) as unknown
      if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
        localError.value = '高级选项必须是 JSON 对象，例如 {"key":"value"}。'
        return
      }
    } catch {
      localError.value = '高级选项不是合法的 JSON。'
      return
    }
  }

  emit('submit', {
    name: form.name.trim(),
    kind: form.kind,
    deviceId: form.deviceId,
    nodeId: form.nodeId,
    clientId: form.clientId.trim(),
    targetIp: form.targetIp.trim(),
    targetPort,
    remotePort,
    domain: form.domain.trim(),
    tags: form.tags.trim(),
    options: rawOptions || '{}',
  })
}
</script>

<template>
  <form novalidate @submit.prevent="submit">
    <div class="form-grid">
      <FormField label="转发类型" for-id="forward-kind">
        <select id="forward-kind" v-model="form.kind" class="select">
          <option v-for="kind in forwardKindOptions" :key="kind.value" :value="kind.value">
            {{ kind.label }}
          </option>
        </select>
      </FormField>

      <FormField label="转发名称" for-id="forward-name" required :error="localError">
        <input id="forward-name" v-model="form.name" class="input" type="text" maxlength="80" autocomplete="off" placeholder="例如：NAS 管理面板" />
      </FormField>

      <FormField label="节点" for-id="forward-node" required :hint="selectedNode ? (nodeSupport ? '该节点已启用所选能力。' : '该节点未启用所选能力，保存会被服务端拒绝。') : '请选择公网中继节点。'">
        <select id="forward-node" v-model="form.nodeId" class="select">
          <option value="">请选择节点</option>
          <option
            v-for="node in nodes"
            :key="node.id"
            :value="node.id"
            :disabled="!nodeSupportsKind(node, form.kind) && node.id !== form.nodeId"
          >
            {{ optionLabel(node) }}
          </option>
        </select>
      </FormField>

      <FormField label="设备" for-id="forward-device" hint="可选，绑定到具体 Agent 设备。">
        <select id="forward-device" v-model="form.deviceId" class="select">
          <option value="">不绑定设备</option>
          <option v-for="device in devices" :key="device.id" :value="device.id">
            {{ device.name }} · {{ device.status === 'online' ? '在线' : '离线' }}
          </option>
        </select>
      </FormField>

      <FormField label="目标地址" for-id="forward-target-ip" :required="needsPortPair" hint="Agent 侧可达的内网地址。">
        <input id="forward-target-ip" v-model="form.targetIp" class="input mono" type="text" maxlength="120" autocomplete="off" placeholder="192.168.1.10" />
      </FormField>

      <FormField label="目标端口" for-id="forward-target-port" :required="needsPortPair">
        <input id="forward-target-port" v-model.number="form.targetPort" class="input" type="number" min="0" max="65535" inputmode="numeric" />
      </FormField>

      <FormField label="公开入口端口" for-id="forward-remote-port" :required="needsPortPair" hint="必须落在节点允许的端口段内。">
        <input id="forward-remote-port" v-model.number="form.remotePort" class="input" type="number" min="0" max="65535" inputmode="numeric" />
      </FormField>

      <FormField label="域名" for-id="forward-domain" :required="needsDomain" hint="域名类转发必填。">
        <input id="forward-domain" v-model="form.domain" class="input mono" type="text" maxlength="200" autocomplete="off" placeholder="nas.ops.example.com" />
      </FormField>

      <FormField label="客户端 ID" for-id="forward-client" hint="隧道 / P2P / 代理类型使用，可选。">
        <input id="forward-client" v-model="form.clientId" class="input mono" type="text" maxlength="120" autocomplete="off" placeholder="可选" />
      </FormField>

      <FormField label="标签" for-id="forward-tags" hint="逗号分隔。">
        <input id="forward-tags" v-model="form.tags" class="input" type="text" maxlength="200" autocomplete="off" placeholder="team:infra, env:prod" />
      </FormField>

      <FormField
        label="高级选项"
        for-id="forward-options"
        span2
        hint='JSON 对象，例如 {"compression":true}。留空时保存为 {}。'
      >
        <textarea id="forward-options" v-model="form.options" class="textarea" rows="4" spellcheck="false" placeholder="{}"></textarea>
      </FormField>
    </div>

    <div class="notice notice-info" style="margin-top: 16px">
      保存只会写入控制面配置。创建后在详情页点击「下发配置」，才会推送到节点和 Agent。
    </div>

    <div v-if="serverError" class="notice notice-danger" role="alert" style="margin-top: 16px">
      {{ serverError }}
    </div>

    <div class="form-actions">
      <button type="button" class="btn" @click="emit('cancel')">取消</button>
      <button type="submit" class="btn btn-primary" :disabled="submitting">
        <Loader2 v-if="submitting" :size="16" class="spin" aria-hidden="true" />
        <Save v-else :size="16" aria-hidden="true" />
        {{ submitting ? '正在保存…' : submitLabel }}
      </button>
    </div>
  </form>
</template>
