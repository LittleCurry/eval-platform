<script setup lang="ts">
import { NCard, NForm, NFormItem, NInput, NButton, NAlert } from 'naive-ui'
</script>

<template>
  <div style="max-width: 400px; margin: 80px auto">
    <NCard title="登录 Eval Platform">
      <NAlert type="info" :show-icon="false" style="margin-bottom: 16px">
        M0 占位页：登录 / RBAC 在 M7 实现
      </NAlert>
      <NForm>
        <NFormItem label="账号"><NInput placeholder="邮箱或用户名" /></NFormItem>
        <NFormItem label="密码"><NInput type="password" placeholder="密码" /></NFormItem>
        <NButton type="primary" block disabled>登录（M7 开放）</NButton>
      </NForm>
    </NCard>
  </div>
</template>