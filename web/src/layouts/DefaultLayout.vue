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
  { label: '运行报告', key: 'runs' },
  { label: '标注工作台', key: 'annotations' },
  { label: '金标打分', key: 'gold' },
  { label: 'Judge 校准', key: 'calibration' },
  { label: '标注闭环', key: 'closure' },
  { label: '配置模板', key: 'profiles' },
  { label: 'A/B 对比', key: 'compare' },
]

const activeKey = computed(() => {
  const name = String(route.name ?? '')
  if (name === 'dataset-detail') return 'datasets'
  if (name === 'run-detail') return 'runs'
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
          @update:value="onSelect"
      />
    </NLayoutHeader>
    <NLayoutContent content-style="padding: 24px; max-width: 1200px; margin: 0 auto">
      <RouterView />
    </NLayoutContent>
  </NLayout>
</template>