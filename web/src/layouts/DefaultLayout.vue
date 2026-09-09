<script setup lang="ts">
import { computed } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'
import {
  NLayout,
  NLayoutHeader,
  NLayoutContent,
  NMenu,
  NText,
  type MenuOption,
} from 'naive-ui'

const route = useRoute()
const router = useRouter()

const menuOptions: MenuOption[] = [
  { label: '概览', key: 'home' },
  { label: '语料库', key: 'corpora' },
  { label: '数据集', key: 'datasets' },
]

const activeKey = computed(() => {
  const name = String(route.name ?? '')
  if (name === 'dataset-detail') return 'datasets'
  return name
})

function onSelect(key: string) {
  router.push({ name: key })
}
</script>

<template>
  <NLayout style="min-height: 100vh">
    <NLayoutHeader bordered style="display: flex; align-items: center; padding: 0 24px">
      <NText strong style="margin-right: 32px">Eval Platform · 评测控制台</NText>
      <NMenu
          mode="horizontal"
          :value="activeKey"
          :options="menuOptions"
          style="flex: 1"
          @update:value="onSelect"
      />
    </NLayoutHeader>
    <NLayoutContent content-style="padding: 24px; max-width: 1200px; margin: 0 auto">
      <RouterView />
    </NLayoutContent>
  </NLayout>
</template>