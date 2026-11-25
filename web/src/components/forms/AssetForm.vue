<script setup lang="ts">
import { reactive } from 'vue'
import FormField from '@/components/ui/FormField.vue'
import type { Asset, AssetInput, Credential } from '@/api/assetManagement'
import type { Device } from '@/api/types'
const props = defineProps<{ initial?: Asset | null; assets: Asset[]; devices: Device[]; credentials: Credential[]; busy: boolean; error: string }>()
const emit = defineEmits<{ save: [value: AssetInput]; cancel: [] }>()
const form = reactive<AssetInput>({ name: '', address: '', port: 22, protocol: 'ssh', viaDeviceId: '', viaAssetId: '', credentialId: '', hostKey: '', tags: '', enabled: true, notes: '', ...props.initial })
function submit() {
  const { name, address, port, protocol, viaDeviceId, viaAssetId, credentialId, hostKey, tags, enabled, notes } = form
  emit('save', { name: name.trim(), address: address.trim(), port, protocol, viaDeviceId, viaAssetId, credentialId, hostKey: hostKey.trim(), tags, enabled, notes })
}
</script>
<template>
  <form @submit.prevent="submit">
    <fieldset :disabled="busy" style="border: 0; padding: 0; margin: 0">
      <div class="form-grid">
        <FormField label="名称" for-id="asset-name" required><input id="asset-name" v-model="form.name" class="input" required maxlength="120" /></FormField>
        <FormField label="地址" for-id="asset-address" required><input id="asset-address" v-model="form.address" class="input" required placeholder="IP 或主机名" /></FormField>
        <FormField label="协议" for-id="asset-protocol"><select id="asset-protocol" v-model="form.protocol" class="select"><option v-for="p in ['ssh', 'rdp', 'vnc', 'http', 'mysql', 'postgres']" :key="p">{{ p }}</option></select></FormField>
        <FormField label="端口" for-id="asset-port" required><input id="asset-port" v-model.number="form.port" class="input" type="number" min="1" max="65535" required /></FormField>
        <FormField label="入口 Agent 设备" for-id="asset-device" hint="留空使用控制面直连或继承跳板链入口。">
          <select id="asset-device" v-model="form.viaDeviceId" class="select"><option value="">无指定设备</option><option v-if="form.viaDeviceId && !devices.some(d => d.id === form.viaDeviceId)" :value="form.viaDeviceId">{{ form.viaDeviceId }}（不可用）</option><option v-for="d in devices" :key="d.id" :value="d.id">{{ d.name }} · {{ d.status }}</option></select>
        </FormField>
        <FormField label="跳板资产" for-id="asset-via" hint="路径由服务端解析；循环引用会被拒绝。">
          <select id="asset-via" v-model="form.viaAssetId" class="select"><option value="">无跳板</option><option v-if="form.viaAssetId && !assets.some(a => a.id === form.viaAssetId)" :value="form.viaAssetId">{{ form.viaAssetId }}（不可用）</option><option v-for="a in assets.filter(a => a.id !== initial?.id)" :key="a.id" :value="a.id">{{ a.name }} · {{ a.protocol }}{{ a.enabled ? '' : '（已禁用）' }}</option></select>
        </FormField>
        <FormField label="绑定凭据" for-id="asset-credential" hint="留空解除绑定。仅加载凭据元数据。">
          <select id="asset-credential" v-model="form.credentialId" class="select"><option value="">未绑定</option><option v-if="form.credentialId && !credentials.some(c => c.id === form.credentialId)" :value="form.credentialId">{{ form.credentialId }}（不可用）</option><option v-for="c in credentials" :key="c.id" :value="c.id">{{ c.name }} · {{ c.username || c.kind }}</option></select>
        </FormField>
        <FormField label="标签" for-id="asset-tags"><input id="asset-tags" v-model="form.tags" class="input" placeholder="逗号分隔" /></FormField>
        <p class="form-hint">SSH 主机指纹请在详情中发现并核对后固定。修改地址、端口或跳板会清除旧指纹。</p>
        <FormField label="备注" for-id="asset-notes" span2><textarea id="asset-notes" v-model="form.notes" class="textarea" rows="3"></textarea></FormField>
        <label><input v-model="form.enabled" type="checkbox" /> 启用资产</label>
      </div>
    </fieldset>
    <p v-if="error" class="notice notice-danger" role="alert">{{ error }}</p>
    <div class="form-actions"><button type="button" class="btn" :disabled="busy" @click="emit('cancel')">取消</button><button class="btn btn-primary" :disabled="busy">{{ busy ? '保存中…' : '保存资产' }}</button></div>
  </form>
</template>
