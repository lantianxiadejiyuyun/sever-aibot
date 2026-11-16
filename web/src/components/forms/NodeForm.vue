<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { Loader2, Save } from 'lucide-vue-next'
import FormField from '@/components/ui/FormField.vue'
import type { Node, NodeFeatures } from '@/api/types'
import type { NodeInput } from '@/api/endpoints'
import { validPortSpec } from '@/utils/node'

const props = withDefaults(
  defineProps<{
    initial?: Node | null
    submitting?: boolean
    serverError?: string
    submitLabel?: string
  }>(),
  { initial: null, submitting: false, serverError: '', submitLabel: '保存节点' },
)

const emit = defineEmits<{
  submit: [payload: NodeInput]
  cancel: []
}>()

interface NodeFormState extends NodeInput {
  features: NodeFeatures
}

function blank(): NodeFormState {
  return {
    name: '',
    remark: '',
    tags: '',
    addr: '',
    protocol: 'tcp',
    httpPort: 0,
    replaceAddress: '',
    forwardPorts: '',
    domain: '',
    urlTemplate: '',
    denyDomainPrefix: '',
    allowWildcard: false,
    features: { web: true, forward: false, tunnel: false, p2p: false, proxy: false },
    maxPoolCount: 0,
    p2pDisableForward: false,
    rules: '',
    limitTotalGb: 0,
    limitKind: '',
    limitResetDay: 0,
    limiterMbps: 0,
    concurrency: 0,
    connections: 0,
  }
}

const form = reactive<NodeFormState>(blank())
const localError = ref('')

function fill(node: Node | null): void {
  const next = blank()
  if (node) {
    next.name = node.name
    next.remark = node.remark
    next.tags = node.tags
    next.addr = node.addr
    next.protocol = node.protocol || 'tcp'
    next.httpPort = node.httpPort
    next.replaceAddress = node.replaceAddress
    next.forwardPorts = node.forwardPorts
    next.domain = node.domain
    next.urlTemplate = node.urlTemplate
    next.denyDomainPrefix = node.denyDomainPrefix
    next.allowWildcard = node.allowWildcard
    next.features = { ...blank().features, ...node.features }
    next.maxPoolCount = node.maxPoolCount
    next.p2pDisableForward = node.p2pDisableForward
    next.rules = node.rules
    next.limitTotalGb = node.limitTotalGb
    next.limitKind = node.limitKind
    next.limitResetDay = node.limitResetDay
    next.limiterMbps = node.limiterMbps
    next.concurrency = node.concurrency
    next.connections = node.connections
  }
  Object.assign(form, next)
}

watch(() => props.initial, fill, { immediate: true })

function numberValue(value: number | string): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function submit(): void {
  localError.value = ''
  if (!form.name.trim()) {
    localError.value = '节点名称不能为空。'
    return
  }
  if (!['tcp', 'kcp', 'quic', 'ws', 'wss'].includes(form.protocol)) {
    localError.value = '连接协议必须是 tcp / kcp / quic / ws / wss。'
    return
  }
  const httpPort = numberValue(form.httpPort)
  if (httpPort < 0 || httpPort > 65535) {
    localError.value = 'HTTP 端口必须在 0-65535 之间（0 表示未设置）。'
    return
  }
  if (!validPortSpec(form.forwardPorts)) {
    localError.value = '端口段格式无效，示例：20000-30000,40000。'
    return
  }
  if (numberValue(form.limitTotalGb) < 0 || numberValue(form.limitResetDay) < 0) {
    localError.value = '流量限制与重置日不能为负数。'
    return
  }
  if (
    numberValue(form.maxPoolCount) < 0 ||
    numberValue(form.limiterMbps) < 0 ||
    numberValue(form.concurrency) < 0 ||
    numberValue(form.connections) < 0
  ) {
    localError.value = '连接池、限速与并发参数不能为负数。'
    return
  }

  emit('submit', {
    name: form.name.trim(),
    remark: form.remark.trim(),
    tags: form.tags.trim(),
    addr: form.addr.trim(),
    protocol: form.protocol,
    httpPort,
    replaceAddress: form.replaceAddress.trim(),
    forwardPorts: form.forwardPorts.trim(),
    domain: form.domain.trim(),
    urlTemplate: form.urlTemplate.trim(),
    denyDomainPrefix: form.denyDomainPrefix.trim(),
    allowWildcard: form.allowWildcard,
    features: { ...form.features },
    maxPoolCount: numberValue(form.maxPoolCount),
    p2pDisableForward: form.p2pDisableForward,
    rules: form.rules.trim(),
    limitTotalGb: numberValue(form.limitTotalGb),
    limitKind: form.limitKind.trim(),
    limitResetDay: numberValue(form.limitResetDay),
    limiterMbps: numberValue(form.limiterMbps),
    concurrency: numberValue(form.concurrency),
    connections: numberValue(form.connections),
  })
}
</script>

<template>
  <form novalidate @submit.prevent="submit">
    <div class="form-grid">
      <FormField label="节点名称" for-id="node-name" required :error="localError">
        <input id="node-name" v-model="form.name" class="input" type="text" maxlength="80" autocomplete="off" placeholder="例如：HK-01 香港入口" />
      </FormField>

      <FormField label="标签" for-id="node-tags" hint="逗号分隔，便于筛选。">
        <input id="node-tags" v-model="form.tags" class="input" type="text" maxlength="200" autocomplete="off" placeholder="region:hk, line:cn2" />
      </FormField>

      <FormField label="接入地址" for-id="node-addr" hint="内网 Agent 拨号的隧道地址，例如 node.example.com:7000。">
        <input id="node-addr" v-model="form.addr" class="input" type="text" maxlength="200" autocomplete="off" placeholder="node.example.com:7000" />
      </FormField>

      <FormField label="连接协议" for-id="node-protocol">
        <select id="node-protocol" v-model="form.protocol" class="select">
          <option value="tcp">TCP</option>
          <option value="kcp">KCP</option>
          <option value="quic">QUIC</option>
          <option value="ws">WebSocket</option>
          <option value="wss">WebSocket TLS</option>
        </select>
      </FormField>

      <FormField label="HTTP 端口" for-id="node-http-port" hint="0 表示未配置。">
        <input id="node-http-port" v-model.number="form.httpPort" class="input" type="number" min="0" max="65535" inputmode="numeric" />
      </FormField>

      <FormField label="替换地址" for-id="node-replace-addr" hint="可选，用于回源或地址改写。">
        <input id="node-replace-addr" v-model="form.replaceAddress" class="input" type="text" maxlength="200" autocomplete="off" placeholder="可选" />
      </FormField>

      <FormField label="可用端口段" for-id="node-forward-ports" hint="示例：20000-30000,40000。端口类转发只允许落在这些区间内。">
        <input id="node-forward-ports" v-model="form.forwardPorts" class="input mono" type="text" maxlength="300" autocomplete="off" placeholder="20000-30000,40000" />
      </FormField>

      <FormField label="节点域名" for-id="node-domain" hint="用于域名类转发的后缀或说明。">
        <input id="node-domain" v-model="form.domain" class="input" type="text" maxlength="200" autocomplete="off" placeholder="ops.example.com" />
      </FormField>

      <FormField label="URL 模板" for-id="node-url-template" span2 hint="可选，支持占位符的自定义访问地址模板。">
        <input id="node-url-template" v-model="form.urlTemplate" class="input mono" type="text" maxlength="300" autocomplete="off" placeholder="https://{domain}" />
      </FormField>

      <FormField label="禁止的域名前缀" for-id="node-deny-prefix" hint="逗号分隔。">
        <input id="node-deny-prefix" v-model="form.denyDomainPrefix" class="input mono" type="text" maxlength="300" autocomplete="off" placeholder="admin, internal" />
      </FormField>

      <FormField label="最大连接池" for-id="node-pool" hint="0 表示使用节点默认值。">
        <input id="node-pool" v-model.number="form.maxPoolCount" class="input" type="number" min="0" inputmode="numeric" />
      </FormField>

      <FormField label="流量上限 (GB)" for-id="node-limit-gb" hint="0 表示不限制。">
        <input id="node-limit-gb" v-model.number="form.limitTotalGb" class="input" type="number" min="0" step="0.1" inputmode="decimal" />
      </FormField>

      <FormField label="计量方式" for-id="node-limit-kind" hint="例如 monthly、total，由节点侧解释。">
        <input id="node-limit-kind" v-model="form.limitKind" class="input" type="text" maxlength="40" autocomplete="off" placeholder="monthly" />
      </FormField>

      <FormField label="重置日" for-id="node-limit-reset" hint="每月重置日期，0 表示不重置。">
        <input id="node-limit-reset" v-model.number="form.limitResetDay" class="input" type="number" min="0" max="31" inputmode="numeric" />
      </FormField>

      <FormField label="限速 (Mbps)" for-id="node-limiter" hint="0 表示不限速。">
        <input id="node-limiter" v-model.number="form.limiterMbps" class="input" type="number" min="0" inputmode="numeric" />
      </FormField>

      <FormField label="并发上限" for-id="node-concurrency" hint="0 表示使用节点默认值。">
        <input id="node-concurrency" v-model.number="form.concurrency" class="input" type="number" min="0" inputmode="numeric" />
      </FormField>

      <FormField label="连接数上限" for-id="node-connections" hint="0 表示使用节点默认值。">
        <input id="node-connections" v-model.number="form.connections" class="input" type="number" min="0" inputmode="numeric" />
      </FormField>

      <FormField label="备注" for-id="node-remark" span2>
        <textarea id="node-remark" v-model="form.remark" class="textarea plain" rows="2" maxlength="500" placeholder="可选备注"></textarea>
      </FormField>
    </div>

    <div class="form-section">
      <div class="form-section-title">能力开关</div>
      <div class="switch-grid">
        <label class="switch">
          <span>Web 域名</span>
          <input v-model="form.features.web" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
        <label class="switch">
          <span>端口转发</span>
          <input v-model="form.features.forward" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
        <label class="switch">
          <span>私有隧道</span>
          <input v-model="form.features.tunnel" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
        <label class="switch">
          <span>P2P 中继</span>
          <input v-model="form.features.p2p" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
        <label class="switch">
          <span>代理能力</span>
          <input v-model="form.features.proxy" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
        <label class="switch">
          <span>P2P 关闭转发</span>
          <input v-model="form.p2pDisableForward" type="checkbox" />
          <span class="switch-track" aria-hidden="true"></span>
        </label>
      </div>
      <label class="checkbox-row" style="margin-top: 10px">
        <input v-model="form.allowWildcard" type="checkbox" />
        <span>
          <strong>允许通配符域名</strong>
          <small>域名类转发可使用 *.domain 形式，仅建议在受信任节点启用。</small>
        </span>
      </label>
    </div>

    <div class="form-section">
      <FormField label="节点规则" for-id="node-rules" hint="可选，节点侧自定义规则文本。">
        <textarea id="node-rules" v-model="form.rules" class="textarea" rows="4" maxlength="2000" placeholder="可选规则内容"></textarea>
      </FormField>
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
