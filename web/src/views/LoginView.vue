<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NSpace, NText } from 'naive-ui'
import { getAuthStatus } from '../api/auth'
import { signIn, signUpFirstAdmin } from '../composables/useSession'
import { safeRedirect } from '../utils/session'

const route = useRoute()
const router = useRouter()

// 空库时显示"创建管理员": 否则装完系统谁也进不去, 只能手动插库
const bootstrapNeeded = ref(false)
const statusLoaded = ref(false)
const statusError = ref('')

const email = ref('')
const name = ref('')
const password = ref('')
const passwordAgain = ref('')
const loading = ref(false)
const errorText = ref('')

const isRegister = computed(() => bootstrapNeeded.value)

onMounted(async () => {
  try {
    const status = await getAuthStatus()
    bootstrapNeeded.value = status.bootstrap_needed
  } catch (err) {
    // 后端不可用时也要给个明确交代, 而不是留一个点了没反应的登录按钮
    statusError.value = err instanceof Error ? err.message : String(err)
  } finally {
    statusLoaded.value = true
  }
})

async function submit() {
  errorText.value = ''
  if (!email.value.trim() || !password.value) {
    errorText.value = '请填写邮箱与口令'
    return
  }
  if (isRegister.value) {
    if (password.value.length < 8) {
      errorText.value = '口令至少 8 位'
      return
    }
    if (password.value !== passwordAgain.value) {
      errorText.value = '两次输入的口令不一致'
      return
    }
  }
  loading.value = true
  try {
    if (isRegister.value) {
      await signUpFirstAdmin(email.value.trim(), name.value.trim(), password.value)
    } else {
      await signIn(email.value.trim(), password.value)
    }
    // 登录后回到被拦下来的那个页面(只接受站内路径, 防开放重定向)
    await router.replace(safeRedirect(route.query.redirect))
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div style="max-width: 460px; margin: 80px auto">
    <NCard :title="isRegister ? '创建管理员账号' : '登录 Eval Platform'">
      <NAlert v-if="isRegister" type="info" :show-icon="false" style="margin-bottom: 16px">
        <NText style="font-size: 13px">
          系统还没有任何账号：第一个账号自动成为<strong>管理员</strong>。
          之后的账号由管理员在「用户管理」里添加（内部工具不开放自助注册）。
        </NText>
      </NAlert>
      <NAlert v-if="statusError" type="warning" :show-icon="false" style="margin-bottom: 12px">
        无法确认系统状态：{{ statusError }}（后端可能没起来）
      </NAlert>
      <NAlert v-if="errorText" type="error" :show-icon="false" style="margin-bottom: 12px">
        {{ errorText }}
      </NAlert>

      <NForm @submit.prevent="submit">
        <NFormItem label="邮箱">
          <NInput
              v-model:value="email"
              placeholder="you@example.com"
              :input-props="{ autocomplete: 'username', type: 'email' }"
              @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem v-if="isRegister" label="名字">
          <NInput v-model:value="name" placeholder="显示名（可留空）" @keyup.enter="submit" />
        </NFormItem>
        <NFormItem label="口令">
          <NInput
              v-model:value="password"
              type="password"
              show-password-on="click"
              :placeholder="isRegister ? '至少 8 位（最长 72 字节）' : '口令'"
              :input-props="{ autocomplete: isRegister ? 'new-password' : 'current-password' }"
              @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem v-if="isRegister" label="再输一次">
          <NInput
              v-model:value="passwordAgain"
              type="password"
              show-password-on="click"
              placeholder="确认口令"
              :input-props="{ autocomplete: 'new-password' }"
              @keyup.enter="submit"
          />
        </NFormItem>
        <NSpace vertical>
          <NButton type="primary" block :loading="loading" :disabled="!statusLoaded" @click="submit">
            {{ isRegister ? '创建并进入' : '登录' }}
          </NButton>
          <NText depth="3" style="font-size: 12px">
            登录状态存在浏览器本地, {{ isRegister ? '创建后' : '登录后' }} 12 小时内免登录；
            被停用或删除的账号会立即失效（每次请求都会回服务端确认）。
          </NText>
        </NSpace>
      </NForm>
    </NCard>
  </div>
</template>
