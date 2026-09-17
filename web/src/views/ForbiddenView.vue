<script setup lang="ts">
// 权限不足页(M7-1)。
//
// 为什么不直接跳登录页: 用户**已经登录**了, 只是角色不够。跳登录页会让他
// 反复登录还是进不去, 以为是密码问题; 说清"缺哪个权限、你现在是什么角色、
// 找谁开"才能让人知道下一步该干什么。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NCard, NSpace, NTag, NText } from 'naive-ui'
import { useSession } from '../composables/useSession'
import { roleLabel, roleTagType } from '../utils/session'

const route = useRoute()
const router = useRouter()
const session = useSession()

const need = computed(() => (typeof route.query.need === 'string' ? route.query.need : ''))
const from = computed(() => (typeof route.query.from === 'string' ? route.query.from : '/'))
</script>

<template>
  <div style="max-width: 560px; margin: 80px auto">
    <NCard title="权限不足">
      <NAlert type="warning" :show-icon="false" style="margin-bottom: 16px">
        <NText style="font-size: 13px">
          这个页面需要
          <strong>{{ need ? roleLabel(need) : '更高' }}</strong>
          权限；你当前是
          <NTag size="small" :type="roleTagType(session.role.value)">
            {{ roleLabel(session.role.value) }}
          </NTag>
          （{{ session.user.value?.email }}）。
        </NText>
      </NAlert>

      <NText depth="3" style="display: block; font-size: 12px; margin-bottom: 16px">
        需要开权限的话，让管理员在「用户管理」里改你的角色；改完你刷新一下即可生效。
      </NText>

      <NSpace>
        <NButton size="small" @click="router.push('/')">回概览</NButton>
        <NButton size="small" quaternary @click="router.back()">返回上一页</NButton>
        <NText depth="3" style="font-size: 12px; line-height: 32px">想访问的页面：{{ from }}</NText>
      </NSpace>
    </NCard>
  </div>
</template>
