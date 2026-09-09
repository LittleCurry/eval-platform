<script setup lang="ts">
import { RouterView } from 'vue-router'
import { NLayout, NLayoutHeader, NLayoutContent, NText } from 'naive-ui'
</script>

<template>
  <NLayout style="min-height: 100vh">
    <NLayoutHeader
        bordered
        style="height: 56px; display: flex; align-items: center; padding: 0 24px"
    >
      <NText strong>Eval Platform · 评测控制台</NText>
    </NLayoutHeader>
    <NLayoutContent content-style="padding: 24px">
      <RouterView />
    </NLayoutContent>
  </NLayout>
</template>