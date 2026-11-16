import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { logout } from '@/stores/session'
import { confirmAction } from '@/composables/useConfirm'
import { useToast } from '@/composables/useToast'
import { errorMessage } from '@/api/client'

export function useLogout() {
  const router = useRouter()
  const toast = useToast()
  const pending = ref(false)

  async function signOut(): Promise<void> {
    const ok = await confirmAction({
      title: '退出登录',
      message: '确认退出当前控制台会话吗？',
      detail: '退出后需要重新输入管理员用户名和密码。',
      confirmText: '退出登录',
      danger: true,
    })
    if (!ok) return
    pending.value = true
    try {
      await logout()
      toast.success('已退出登录')
      await router.replace('/login')
    } catch (error) {
      toast.error('退出登录失败', errorMessage(error))
    } finally {
      pending.value = false
    }
  }

  return { signOut, pending }
}
