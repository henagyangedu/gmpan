<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { OfficeBuilding, UploadFilled, Refresh, Check } from '@element-plus/icons-vue'
import ScanSettings from './ScanSettings.vue'
const props = defineProps<{ api: (path: string, method?: string, body?: any) => Promise<any>; logoUrl: string }>()
const emit = defineEmits<{ updated: [] }>()
const loading = ref(true), saving = ref(false), logoBusy = ref(false), smsSaving = ref(false)
const tab = ref('site'), logoInput = ref<HTMLInputElement>()
const site = ref({ company_name: '', company_short_name: '', copyright: '', copyright_url: '', default_theme: 'explorer', storage_root: '', storage_warning_percent: 10, listen_port:0, revision: 0 })
const runtime = ref({ active_storage_root: '', deployment_dir: '', migration_required: false, previous_data_dir: '', previous_source_dir: '', restart_required: false, has_logo: false,active_listen:'',active_port:0,active_scheme:'',can_restart:false,instance_id:'' })
const directoryDialog=ref(false),directoryLoading=ref(false),directoryInput=ref('')
const directories=ref({path:'',parent:'',items:[] as {name:string;path:string}[],selectable:false,truncated:false})
const portDialog=ref(false),portSaving=ref(false),port=ref(18080),restarting=ref(false),restartURL=ref(''),savedSite=ref('')
const defaultWebsitePort=computed(()=>(runtime.value.active_scheme || location.protocol.slice(0,-1))==='https'?443:80)
const rescanNotice='目录变更后，请重新扫描新目录中的资料；扫描完成前，新增或变更的文件可能无法搜索到。'
const directoryChanged=computed(()=>site.value.storage_root!==runtime.value.active_storage_root)
function siteSnapshot(){const {revision,listen_port,...fields}=site.value;return JSON.stringify(fields)}
const dirty=computed(()=>siteSnapshot()!==savedSite.value)
const sms = ref({ enabled: false, base_url: '', template_key: '', has_app_key: false, has_app_secret: false, revision: 0 })
const credentials = ref({ app_key: '', app_secret: '', clear_credentials: false })
const savedSMS = ref(''), smsTesting = ref(false), testMobile = ref(''), testCooldown = ref(0), testError = ref('')
const testResult = ref<{accepted:boolean;message:string;mobile_masked:string;platform_code?:number;http_status?:number}>()
let testTimer: ReturnType<typeof setInterval> | undefined
function smsSnapshot() { return JSON.stringify({enabled:sms.value.enabled,base_url:sms.value.base_url,template_key:sms.value.template_key}) }
const smsDirty = computed(() => smsSnapshot() !== savedSMS.value || !!credentials.value.app_key || !!credentials.value.app_secret || credentials.value.clear_credentials)
const smsTestConfigured = computed(() => sms.value.has_app_key && sms.value.has_app_secret && !!sms.value.base_url && !!sms.value.template_key)
function applySMS(value:typeof sms.value) { sms.value=value; savedSMS.value=smsSnapshot(); credentials.value={app_key:'',app_secret:'',clear_credentials:false}; testResult.value=undefined; testError.value='' }
const themes = [{ id: 'explorer', name: '目录工作台', note: 'A · 电脑目录与紧凑列表' }, { id: 'library', name: '影像资料库', note: 'B · 视频卡片与资源浏览' }, { id: 'classic', name: '清爽协作', note: '原有绿色工作空间' }]
function error(e: any) { ElMessage.error(e.message || '操作未完成') }
async function load() { loading.value = true; try { const [s, m] = await Promise.all([props.api('/admin/site-settings'), props.api('/admin/sms-settings')]); site.value = s.settings; runtime.value = s;savedSite.value=siteSnapshot(); applySMS(m) } catch (e) { error(e) } finally { loading.value = false } }
async function save() { saving.value = true; try { const result = await props.api('/admin/site-settings', 'PUT', site.value); site.value = result.settings; runtime.value = result;savedSite.value=siteSnapshot(); emit('updated'); ElMessage.success(result.restart_required ? '站点配置已保存，重启站点后生效' : '站点配置已生效');if(directoryChanged.value)ElMessage.warning(rescanNotice) } catch (e) { error(e) } finally { saving.value = false } }
async function browse(path=''){directoryLoading.value=true;try{directories.value=await props.api('/admin/directories?path='+encodeURIComponent(path));directoryInput.value=directories.value.path}catch(e){error(e)}finally{directoryLoading.value=false}}
async function chooseDirectory(){directoryDialog.value=true;await browse(site.value.storage_root || runtime.value.active_storage_root)}
function selectDirectory(){site.value.storage_root=directories.value.path;directoryDialog.value=false}
function changePort(){port.value=site.value.listen_port || runtime.value.active_port;portDialog.value=true}
async function savePort(){portSaving.value=true;try{const result=await props.api('/admin/listen-port','PUT',{port:port.value,revision:site.value.revision});runtime.value=result;site.value.listen_port=result.settings.listen_port;site.value.revision=result.settings.revision;portDialog.value=false;ElMessage.success('监听端口已保存，重启站点后生效')}catch(e){error(e)}finally{portSaving.value=false}}
async function restart(){
  try{await ElMessageBox.confirm('站点连接会短暂中断，未完成上传可在恢复后续传。确认重启 GmPan 站点？','重启站点',{type:'warning',confirmButtonText:'确认重启',cancelButtonText:'取消'})}catch{return}
  restarting.value=true
  try{
    const result=await props.api('/admin/restart','POST',{revision:site.value.revision})
    const target=new URL(location.href);target.port=String(result.port || runtime.value.active_port);target.pathname='/';target.search='';target.hash='';restartURL.value=target.href
    await new Promise(resolve=>setTimeout(resolve,1500))
    if(target.origin!==location.origin){ElMessage.success('重启请求已提交，端口变更后请点击下方新地址访问');return}
    for(let attempt=0;attempt<45;attempt++){
      try{
        const response=await fetch(target.origin+'/api/health',{credentials:'omit',cache:'no-store',signal:AbortSignal.timeout(2000)})
        const state=await response.json();if(response.ok && state.status==='ok' && state.instance_id!==result.instance_id){location.assign(target.href);return}
      }catch{}
      await new Promise(resolve=>setTimeout(resolve,1000))
    }
    ElMessage.warning('暂未连接到新站点，请稍后点击访问地址，或查看服务器日志')
  }catch(e){error(e)}finally{restarting.value=false}
}
async function changeLogo(event: Event) { const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (!file) return; if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > 2 * 1024 * 1024) { ElMessage.warning('请选择 2 MB 以内的 PNG 或 JPEG 图片'); return }; logoBusy.value = true; try { const result = await props.api('/admin/site-logo', 'PUT', file); site.value.revision = result.settings.revision; runtime.value = result; emit('updated'); ElMessage.success('公司 Logo 已更新') } catch (e) { error(e) } finally { logoBusy.value = false } }
async function removeLogo() { logoBusy.value = true; try { const result = await props.api('/admin/site-logo', 'DELETE'); site.value.revision = result.settings.revision; runtime.value = result; emit('updated'); ElMessage.success('已恢复默认图标') } catch (e) { error(e) } finally { logoBusy.value = false } }
async function saveSMS() { if(smsTesting.value)return; smsSaving.value = true; try { const { has_app_key, has_app_secret, ...fields } = sms.value; applySMS(await props.api('/admin/sms-settings', 'PUT', { ...fields, ...credentials.value })); emit('updated'); ElMessage.success('短信配置已生效') } catch (e) { error(e) } finally { smsSaving.value = false } }
async function testSMS() {
  if(smsTesting.value || smsSaving.value || testCooldown.value)return
  testResult.value=undefined;testError.value=''
  if(smsDirty.value){testError.value='短信配置有未保存的修改，请先保存再测试。';return}
  if(!/^1[3-9]\d{9}$/.test(testMobile.value)){testError.value='请输入有效的测试手机号';return}
  smsTesting.value=true
  try{
    const result=await props.api('/admin/sms-test','POST',{mobile:testMobile.value,revision:sms.value.revision})
    testResult.value=result;testCooldown.value=result.cooldown_seconds || 60
    if(testTimer)clearInterval(testTimer)
    testTimer=setInterval(()=>{testCooldown.value=Math.max(0,testCooldown.value-1);if(!testCooldown.value){clearInterval(testTimer);testTimer=undefined}},1000)
  }catch(e:any){testError.value=e.message || '测试请求结果未确认，请先检查手机及平台发送记录，不要连续重发。'}finally{smsTesting.value=false}
}
onMounted(load)
onUnmounted(()=>{if(testTimer)clearInterval(testTimer)})
</script>
<template>
  <section v-loading="loading" class="site-settings files-panel">
    <div class="section-title"><h3>站点配置</h3><el-button :icon="Refresh" @click="load">重新加载</el-button></div>
    <el-tabs v-model="tab" class="settings-tabs"><el-tab-pane label="站点与目录" name="site"/><el-tab-pane label="病毒检查" name="scan"/><el-tab-pane label="短信配置" name="sms"/></el-tabs>
    <ScanSettings v-if="tab==='scan'" :api="api" @updated="(revision:number)=>{site.revision=revision;emit('updated')}"/>
    <el-form v-if="tab === 'site'" label-position="top" class="settings-form" @submit.prevent="save">
      <h3>公司标识</h3><div class="form-two"><el-form-item label="公司名称"><el-input v-model="site.company_name" maxlength="100" show-word-limit /></el-form-item><el-form-item label="公司简称"><el-input v-model="site.company_short_name" maxlength="24" show-word-limit /></el-form-item></div>
      <el-form-item label="公司 Logo"><div class="logo-settings"><div class="logo-preview"><img v-if="logoUrl" :src="logoUrl" alt="公司 Logo"><el-icon v-else :size="32"><OfficeBuilding/></el-icon></div><div><el-button :icon="UploadFilled" :loading="logoBusy" @click="logoInput?.click()">上传 Logo</el-button><el-button v-if="runtime.has_logo" :disabled="logoBusy" @click="removeLogo">恢复默认图标</el-button><p class="subtle">PNG / JPEG，最大 2 MB，宽高均不超过 1024 像素；上传后立即生效。</p></div></div><input ref="logoInput" type="file" accept="image/png,image/jpeg" hidden @change="changeLogo"></el-form-item>
      <h3>默认主题</h3><div class="theme-options"><button v-for="t in themes" :key="t.id" type="button" :class="['theme-option', t.id, { selected: site.default_theme === t.id }]" :aria-pressed="site.default_theme === t.id" @click="site.default_theme = t.id"><span class="theme-swatch"><i/><i/><i/></span><strong>{{ t.name }}<el-icon v-if="site.default_theme === t.id"><Check/></el-icon></strong><small>{{ t.note }}</small></button></div><p class="subtle">员工可在页面右上角选择自己的主题，或跟随站点默认。</p>
      <h3>文件与部署目录</h3>
      <el-alert v-if="runtime.restart_required" type="warning" title="运行配置已保存，等待重启站点生效" :closable="false" show-icon/>
      <el-alert v-if="runtime.migration_required" type="warning" title="旧目录配置等待统一迁移" :closable="false" show-icon><p>原上传目录：{{ runtime.previous_data_dir }}<br>原公司资料目录：{{ runtime.previous_source_dir || '未配置' }}<br>已有文件时请先备份、停服并执行目录迁移；迁移完成前仍从原路径读取。</p></el-alert>
      <el-form-item label="文件存储和读取目录"><el-input :model-value="site.storage_root" readonly placeholder="点击选择服务器目录" @click="chooseDirectory"><template #append><el-button @click.stop="chooseDirectory">变更目录</el-button></template></el-input><div class="subtle path-description">当前生效：{{ runtime.active_storage_root || '尚未统一，保持原目录' }}<br>选择的是部署服务器上的目录。公司资料在此目录扫描，系统上传文件保存在内部 .gmpan 目录，按空间权限隔离。</div></el-form-item>
      <el-alert v-if="directoryChanged" :title="rescanNotice" type="warning" :closable="false" show-icon/>
      <el-form-item label="站点部署目录"><el-input :model-value="runtime.deployment_dir" readonly/><div class="subtle path-description">正在运行的程序所在目录，只读显示。程序、配置、证书和日志按部署手册放置；移动站点需停服操作。</div></el-form-item>
      <el-form-item label="剩余空间告警阈值"><el-input-number v-model="site.storage_warning_percent" :min="1" :max="99" :precision="0"/><span class="threshold-unit">%</span><div class="subtle path-description">默认 10%。文件目录所在磁盘的可用空间低于此比例时，在系统页面提醒维护；每分钟更新一次。</div></el-form-item>
      <p class="settings-note">这里填写服务器上的路径。已有文件或索引时不能直接换到另一目录，需备份后停服迁移；迁移会复制并校验文件，保留原文件及权限、备注。文件目录不能与站点程序、配置、证书或日志目录重叠。</p>
      <h3>站点运行</h3><el-form-item label="监听地址与端口"><el-input :model-value="runtime.active_listen" readonly><template #append><el-button @click="changePort">修改监听端口</el-button></template></el-input><div class="subtle path-description">当前端口：{{ runtime.active_port }}<span v-if="site.listen_port && site.listen_port !== runtime.active_port">；待生效端口：{{ site.listen_port }}</span>。修改后需重启站点，访问地址随端口变化。</div></el-form-item><p class="settings-note">内网访问需确认新端口已被服务器防火墙允许；使用图形部署新版程序规则时可随端口切换。</p>
      <h3>底部版权</h3><el-form-item label="版权说明"><el-input v-model="site.copyright" maxlength="200" placeholder="长沙感梦科技有限公司 版权所有"/></el-form-item><el-form-item label="版权关联网址"><el-input v-model="site.copyright_url" placeholder="https://www.gmkj.cc/"/></el-form-item><div class="settings-actions"><el-button type="primary" native-type="submit" :loading="saving">保存站点配置</el-button></div>
    </el-form>
    <div v-if="tab==='site'" class="settings-actions"><el-button type="warning" :loading="restarting" :disabled="!runtime.can_restart || dirty || saving" @click="restart">重启站点</el-button><span v-if="dirty" class="subtle">有未保存的修改，请先保存或重新加载。</span><span v-else-if="!runtime.can_restart" class="subtle">当前服务程序尚未启用重启功能，请更新服务程序。</span></div>
    <el-alert v-if="restarting || restartURL" type="info" :closable="false" title="站点重启与重新连接"><p>{{ restarting ? '正在等待站点恢复，成功后将自动跳转。' : '可点击下方地址重新访问。' }}</p><a v-if="restartURL" :href="restartURL">{{ restartURL }}</a></el-alert>
    <el-form v-if="tab==='sms'" label-position="top" class="settings-form" @submit.prevent="saveSMS"><h3>短信登录</h3><el-form-item label="启用短信验证码登录"><el-switch v-model="sms.enabled" active-text="启用" inactive-text="关闭"/></el-form-item><el-form-item label="短信平台接口基址"><el-input v-model="sms.base_url" placeholder="https://sms.gmwang.com.cn/api/v1"/></el-form-item><div class="form-two"><el-form-item label="AK（AppKey）"><el-input v-model="credentials.app_key" type="password" show-password autocomplete="new-password" :placeholder="sms.has_app_key ? '已配置，留空保留原值' : '填写平台分配的 AppKey'"/></el-form-item><el-form-item label="SK（AppSecret）"><el-input v-model="credentials.app_secret" type="password" show-password autocomplete="new-password" :placeholder="sms.has_app_secret ? '已配置，留空保留原值' : '填写平台分配的 AppSecret'"/></el-form-item></div><el-form-item label="模板标识 templateKey"><el-input v-model="sms.template_key" maxlength="64" placeholder="例如 standard_code"/></el-form-item><el-checkbox v-model="credentials.clear_credentials">清除已保存的 AppKey 和 AppSecret（需关闭短信登录）</el-checkbox><p class="settings-note">凭证仅保存在服务端，保存后不回显。请使用独立项目凭证，并在短信平台配置模板授权、额度与出口 IP 白名单。保存配置不会发送短信。登录验证码为 6 位数字，5 分钟有效，成功后一次作废；错误 5 次失效。重启站点或修改短信配置后需重新获取。</p><p class="subtle">关闭短信登录后，员工仍可使用用户名或绑定手机号加密码登录。</p><div class="settings-actions"><el-button type="primary" native-type="submit" :loading="smsSaving" :disabled="smsTesting">保存短信配置</el-button></div></el-form>
    <el-form v-if="tab==='sms'" label-position="top" class="settings-form sms-test-panel" @submit.prevent="testSMS">
      <h3>发送测试验证码</h3>
      <p class="settings-note">使用已保存的配置和模板标识 templateKey，向下方手机号发送 1 条真实验证码短信，可能消耗平台额度。测试手机号不必绑定员工；测试不会创建账号或登录。可在开启员工短信登录前先测试。</p>
      <el-alert v-if="smsDirty" title="短信配置有未保存的修改，请先保存再测试。" type="info" :closable="false"/>
      <el-alert v-else-if="!smsTestConfigured" title="请先保存接口基址、AppKey、AppSecret 和模板标识 templateKey。" type="info" :closable="false"/>
      <div class="sms-test-actions"><el-form-item label="测试手机号"><el-input v-model="testMobile" maxlength="11" inputmode="tel" autocomplete="off" placeholder="输入接收测试验证码的手机号" :disabled="smsTesting"/></el-form-item><el-button type="primary" native-type="submit" :loading="smsTesting" :disabled="smsDirty || !smsTestConfigured || smsSaving || loading || testCooldown>0">{{ testCooldown ? testCooldown+' 秒后可重试' : '发送测试验证码' }}</el-button></div>
      <el-alert v-if="testError" :title="testError" type="error" :closable="false" show-icon/>
      <el-alert v-if="testResult" :title="testResult.accepted?'平台已受理':'测试发送未成功'" :type="testResult.accepted?'success':'error'" :closable="false" show-icon><p>{{testResult.message}}</p><p>接收号码：{{testResult.mobile_masked}}<template v-if="testResult.platform_code!==undefined">；平台返回码：{{testResult.platform_code}}</template><template v-if="testResult.http_status">；HTTP 状态：{{testResult.http_status}}</template></p></el-alert>
      <p class="subtle">同一手机号与登录验证码共用 60 秒冷却；每位管理员每 10 分钟最多测试 5 次。请求超时不自动重发。测试失败时请按返回码检查配置；平台受理成功不代表手机已送达。</p>
    </el-form>
  </section>
  <el-dialog v-model="directoryDialog" title="选择服务器文件目录" width="min(660px, 94vw)"><p class="subtle">浏览部署服务器的目录。只有空站点能直接变更；已有文件或索引时需停服迁移。</p><div class="directory-toolbar"><el-input v-model="directoryInput" placeholder="输入服务器完整路径后点击浏览" @keyup.enter="browse(directoryInput)"/><el-button :loading="directoryLoading" @click="browse(directoryInput)">浏览</el-button></div><div class="directory-toolbar"><el-button @click="browse('')">服务器磁盘</el-button><el-button :disabled="!directories.path" @click="browse(directories.parent)">上一级</el-button><span class="subtle">{{ directories.path || '选择磁盘' }}</span></div><el-table v-loading="directoryLoading" :data="directories.items" max-height="330" empty-text="没有可浏览的子目录" @row-dblclick="(row:any)=>browse(row.path)"><el-table-column prop="name" label="目录名称"/><el-table-column width="90"><template #default="{row}"><el-button link type="primary" @click="browse(row.path)">进入</el-button></template></el-table-column></el-table><p v-if="directories.truncated" class="subtle">目录内容较多，仅显示部分子目录；可输入完整路径后浏览。</p><el-alert v-if="!directories.selectable && directories.path" title="当前目录不能作为文件根目录，请进入独立的资料文件夹。" type="info" :closable="false"/><template #footer><el-button @click="directoryDialog=false">取消</el-button><el-button type="primary" :disabled="!directories.selectable || directoryLoading" @click="selectDirectory">选择当前目录</el-button></template></el-dialog>
  <el-dialog v-model="portDialog" title="修改监听端口" width="min(460px, 94vw)"><el-form label-position="top" @submit.prevent="savePort"><el-form-item label="新监听端口"><el-input-number v-model="port" :min="1" :max="65535" :precision="0"/><el-button text @click="port=defaultWebsitePort">使用默认端口 {{defaultWebsitePort}}</el-button></el-form-item><p class="subtle">当前协议默认使用 {{defaultWebsitePort}} 端口，默认端口的网址无需填写端口号；只有端口冲突时才需更换。修改端口不会切换 HTTP/HTTPS，保存后需重启站点，并确认防火墙允许新端口。</p><el-button type="primary" native-type="submit" :loading="portSaving">保存端口</el-button></el-form></el-dialog>
</template>

<style scoped>
.sms-test-panel{border-top:1px solid var(--gp-line);margin-top:8px}.sms-test-actions{display:flex;align-items:center;gap:14px;flex-wrap:wrap;margin-top:18px;max-width:640px}.sms-test-actions .el-form-item{flex:1;min-width:220px}.sms-test-panel .el-alert p{margin:5px 0;overflow-wrap:anywhere}
.directory-toolbar{display:flex;gap:10px;align-items:center;margin:14px 0;overflow-wrap:anywhere}.directory-toolbar .el-input{flex:1}.settings-actions{flex-wrap:wrap;gap:12px}.site-settings>.el-alert{margin-top:16px}
</style>
