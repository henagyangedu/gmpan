<script setup lang="ts">
import { computed,onMounted,ref } from 'vue'
import { ElMessage,ElMessageBox } from 'element-plus'
type Service={name:string;path:string;executable:string;directory:string;status:string;exe_exists:boolean;eligible:boolean}
const props=defineProps<{api:(path:string,data?:unknown)=>Promise<any>}>()
const emit=defineEmits<{started:[]}>()
const loading=ref(false),busy=ref(false),error=ref(''),confirmed=ref(false),services=ref<Service[]>([]),selected=ref<string[]>([])
const chosen=computed(()=>services.value.filter(v=>selected.value.includes(v.name)&&v.eligible))
async function load(){loading.value=true;error.value='';confirmed.value=false;try{const r=await props.api('uninstall-info');services.value=r.services;const app=services.value.find(v=>v.name==='GmPan');selected.value=services.value.filter(v=>v.eligible&&(v.name==='GmPan'||!app||v.directory.toLowerCase()===app.directory.toLowerCase())).map(v=>v.name)}catch(e:any){error.value=e.message}finally{loading.value=false}}
async function uninstall(){
  try{await ElMessageBox.confirm('选中服务将停止，员工访问会中断。只移除服务及对应防火墙规则，保留数据库、资料和配置。确认继续？','卸载服务（保留数据）',{type:'warning',confirmButtonText:'确认卸载',cancelButtonText:'取消'})}catch{return}
  busy.value=true;try{await props.api('uninstall',{confirmed:confirmed.value,services:chosen.value.map(v=>({name:v.name,path:v.path}))});emit('started')}catch(e:any){ElMessage.error(e.message)}finally{busy.value=false}
}
onMounted(load)
</script>
<template>
  <section v-loading="loading" class="setup-progress"><h2>卸载 / 清理旧服务</h2>
    <el-alert title="保留全部数据" description="停止并移除所选 GmPan 服务和对应防火墙规则，不删除数据库、上传文件、原始资料、配置、证书或日志。程序已删除的残留服务也可检测清理。" type="warning" :closable="false" show-icon/>
    <el-alert v-if="error" :title="error" type="error" :closable="false"/>
    <el-empty v-else-if="!loading&&!services.length" description="没有检测到 GmPan 服务，无需卸载"/>
    <div v-for="service in services" :key="service.name" class="setup-uninstall-service"><el-checkbox-group v-model="selected"><el-checkbox :value="service.name" :disabled="!service.eligible">{{service.name}} · {{service.status}}</el-checkbox></el-checkbox-group><p>启动路径：{{service.path || '无法读取'}}<br>程序文件：{{service.exe_exists?'存在':'已删除或不可读取'}}<br>保留目录：{{service.directory || '请人工核对'}}</p><el-alert v-if="!service.eligible" title="该项服务路径无法确认属于本部署包，或正等待删除；请按常见问题人工处理，不会自动移除。" type="info" :closable="false"/></div>
    <p>卸载后若需恢复旧站点，应保留并恢复原数据库及配置。全新安装向导不能用于接管非空数据库或已有 .gmpan 的资料目录。</p>
    <el-checkbox v-if="chosen.length" v-model="confirmed">我已确认所选服务及路径，允许停止服务并保留全部数据</el-checkbox>
    <div class="setup-actions"><el-button :disabled="busy" @click="load">重新检测</el-button><el-button type="danger" :loading="busy" :disabled="!chosen.length||!confirmed" @click="uninstall">卸载所选服务（保留数据）</el-button></div>
  </section>
</template>
<style scoped>.setup-uninstall-service{margin-top:20px;padding:16px;background:#f2f5fa;border-radius:10px}.setup-uninstall-service p{overflow-wrap:anywhere;margin-bottom:0}</style>
