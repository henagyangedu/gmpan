<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
const props=defineProps<{api:(path:string,method?:string,body?:any)=>Promise<any>}>()
const emit=defineEmits<{updated:[revision:number]}>()
const loading=ref(true),saving=ref(false),configured=ref(false),revision=ref(0),active=ref(false)
const form=ref({mode:'immediate',start:'22:00',end:'06:00'})
function apply(v:any){form.value=v.settings;revision.value=v.revision;configured.value=v.scanner_configured;active.value=v.active_now}
async function load(){loading.value=true;try{apply(await props.api('/admin/scan-settings'))}catch(e:any){ElMessage.error(e.message)}finally{loading.value=false}}
async function save(){saving.value=true;try{const result=await props.api('/admin/scan-settings','PUT',{...form.value,revision:revision.value});apply(result);emit('updated',result.revision);ElMessage.success('病毒检查设置已保存，立即生效')}catch(e:any){ElMessage.error(e.message)}finally{saving.value=false}}
onMounted(load)
</script>
<template>
  <el-form v-loading="loading" label-position="top" class="settings-form" @submit.prevent="save">
    <h3>病毒检查</h3><p class="settings-note">目录扫描负责登记文件；病毒检查按下方策略独立运行，不影响目录登记和搜索。</p>
    <el-form-item label="检查模式"><el-radio-group v-model="form.mode"><el-radio-button value="off">不启用</el-radio-button><el-radio-button value="immediate">立即检查</el-radio-button><el-radio-button value="scheduled">定时检查</el-radio-button></el-radio-group></el-form-item>
    <div v-if="form.mode==='scheduled'" class="form-two"><el-form-item label="每天开始时间"><el-time-select v-model="form.start" start="00:00" end="23:59" step="00:15" placeholder="开始时间"/></el-form-item><el-form-item label="每天结束时间"><el-time-select v-model="form.end" start="00:00" end="23:59" step="00:15" placeholder="结束时间"/></el-form-item></div>
    <p v-if="form.mode==='scheduled'" class="subtle">按北京时间（UTC+8）执行，支持跨午夜，例如 22:00—次日 06:00。时间段结束会暂停当前检查并放回队列，下次从该文件重新检查。</p>
    <el-alert v-if="form.mode!=='immediate'" title="未检查文件允许预览和下载" description="不启用或定时模式下，未检查文件标注“未执行病毒检查”，允许有权限的员工访问；已发现威胁或检查失败的文件继续阻止访问。未检查不代表安全。" type="warning" :closable="false" show-icon/>
    <el-alert v-else title="检查通过后才允许预览和下载" description="立即模式持续处理待检查队列，未通过检查的文件不能预览、下载或发布。" type="info" :closable="false"/>
    <p v-if="!configured" class="settings-note">当前未配置病毒检查程序。不启用模式不需要杀毒组件；立即或定时模式需先按部署手册配置 ClamAV 和病毒库，否则队列将保持等待。</p>
    <p class="subtle">当前状态：{{ !configured?'检查程序未配置':active?'允许处理检查队列':'检查已关闭或不在设定时间段' }}。设置只影响后续访问和检查；不能收回员工已下载的文件。</p>
    <div class="settings-actions"><el-button type="primary" native-type="submit" :loading="saving">保存病毒检查设置</el-button><el-button @click="load">重新加载</el-button></div>
  </el-form>
</template>
