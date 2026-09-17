<script setup lang="ts">
// 快捷键帮助弹窗(M7-3 打磨): 标注工作台与人工金标页共用一份展示。
//
// 为什么要有它: 以前快捷键只写在页面最底下那一行浅灰小字里 —— 新同事根本看不到,
// 看得到的也得眯着眼一行行读。现在底部那行只说"有哪些键", 细节点这里(或按 ?)看:
// 分组列表 + 键位用真正的键帽样式, 扫一眼就懂。

import { NCard, NModal, NSpace, NTag, NText } from 'naive-ui'
import type { ShortcutGroup } from '../utils/shortcuts'

defineProps<{
    show: boolean
    groups: ShortcutGroup[]
    /** 一句"这个页面专属"的说明(可选) */
    note?: string
}>()

const emit = defineEmits<{ 'update:show': [value: boolean] }>()
</script>

<template>
    <NModal :show="show" @update:show="(value: boolean) => emit('update:show', value)">
        <NCard style="width: 460px" title="快捷键" :bordered="false" size="huge" role="dialog">
            <div v-for="group in groups" :key="group.title" style="margin-bottom: 14px">
                <NText strong style="display: block; margin-bottom: 6px">{{ group.title }}</NText>
                <div v-for="item in group.items" :key="item.action" class="shortcut-row">
                    <NSpace :size="4">
                        <NTag v-for="key in item.keys" :key="key" size="small" :bordered="false" code>
                            {{ key }}
                        </NTag>
                    </NSpace>
                    <NText depth="3">{{ item.action }}</NText>
                </div>
            </div>
            <NText depth="3" style="font-size: 12px">
                在输入框里打字不会被快捷键抢走；带 Ctrl / Cmd / Alt 的组合键也不会被拦。
            </NText>
            <NText v-if="note" depth="3" style="display: block; margin-top: 4px; font-size: 12px">
                {{ note }}
            </NText>
        </NCard>
    </NModal>
</template>

<style scoped>
.shortcut-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 3px 0;
}
</style>
