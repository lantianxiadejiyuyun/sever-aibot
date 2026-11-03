<script setup>
import { onMounted, ref, shallowRef } from 'vue'
import { api } from './store'

const message = ref('正在检查推送状态…')
const busy = ref(true)
const key = ref('')
const subscription = shallowRef(null)
const canEnable = ref(false)
let registration

async function registerOnServer(sub) {
  const result = await api('/push/subscriptions', 'POST', sub.toJSON())
  if (result.subscribed !== true) throw Error('服务端未确认订阅，请重试。')
}

async function refresh() {
  busy.value = true
  canEnable.value = false
  try {
    if (!window.isSecureContext) {
      message.value = '当前页面不是安全连接。请通过 HTTPS 打开工作台以启用推送。'
      return
    }
    if (!navigator.serviceWorker || !('PushManager' in window) || !('Notification' in window)) {
      message.value = '当前浏览器不支持后台推送。在 iPhone 上可尝试添加到主屏幕后打开。'
      return
    }
    await navigator.serviceWorker.register('/sw.js')
    registration = await navigator.serviceWorker.ready
    subscription.value = await registration.pushManager.getSubscription()
    const config = await api('/push/key')
    key.value = config.public_key || ''
    if (!config.enabled || !key.value) {
      message.value = config.reason || '服务端尚未启用 Web Push。'
      return
    }
    if (Notification.permission === 'denied') {
      message.value = '浏览器已禁止通知。请在站点设置中允许通知后刷新状态。'
      return
    }
    if (subscription.value) {
      // Re-register idempotently in case browser subscription data was rotated.
      await registerOnServer(subscription.value)
      message.value = '已订阅本机通知。关闭工作台后仍可收到新异常和恢复通知。'
    } else {
      canEnable.value = true
      message.value = '本机尚未订阅。启用后，关闭工作台仍可接收异常与恢复通知。'
    }
  } catch (e) {
    message.value = '无法确认订阅状态：' + (e.message || '请重试。')
  } finally {
    busy.value = false
  }
}

async function enable() {
  busy.value = true
  try {
    // Invoke from the click itself so mobile browsers retain user activation.
    if (await Notification.requestPermission() !== 'granted') {
      message.value = Notification.permission === 'denied'
        ? '通知权限已被拒绝。请在站点设置中允许后刷新状态。'
        : '尚未获得通知权限，本次未启用。'
      canEnable.value = Notification.permission !== 'denied'
      return
    }
    try {
      subscription.value = await registration.pushManager.subscribe({
        userVisibleOnly: true, applicationServerKey: key.value,
      })
    } catch {
      message.value = '浏览器未能建立推送订阅，请检查网络及系统通知设置后重试。'
      return
    }
    canEnable.value = false
    await registerOnServer(subscription.value)
    message.value = '已订阅本机通知。关闭工作台后仍可收到新异常和恢复通知。'
  } catch (e) {
    message.value = '订阅未完成：' + (e.message || '请刷新状态后重试。')
  } finally {
    busy.value = false
  }
}

async function disable() {
  busy.value = true
  try {
    const sub = subscription.value
    if (!sub) return
    const result = await api('/push/subscriptions', 'DELETE', {endpoint: sub.endpoint})
    if (result.subscribed !== false) throw Error('服务端未确认取消。')
    await sub.unsubscribe()
    subscription.value = await registration.pushManager.getSubscription()
    if (subscription.value) throw Error('浏览器仍保留订阅，请重试取消。')
    canEnable.value = !!key.value && Notification.permission !== 'denied'
    message.value = '本机通知已取消，其他设备的订阅不受影响。'
  } catch (e) {
    message.value = '取消未完成：' + (e.message || '请重试。')
  } finally {
    busy.value = false
  }
}

onMounted(refresh)
</script>
<template>
  <h3>本机异常通知</h3>
  <p class="hint" role="status">{{ message }}</p>
  <p class="hint">异常与恢复记录也可在巡检报告中查看。</p>
  <div class="actions">
    <button v-if="subscription" :disabled="busy" @click="disable">取消本机订阅</button>
    <button v-else-if="canEnable" :disabled="busy" @click="enable">启用本机通知</button>
    <button class="small" :disabled="busy" @click="refresh">刷新通知状态</button>
  </div>
</template>