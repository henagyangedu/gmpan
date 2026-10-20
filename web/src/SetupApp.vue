<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import SetupUninstall from './components/SetupUninstall.vue'

const mode=ref('install')
const token = location.hash.slice(1)
history.replaceState(null, '', location.pathname)
const info = ref({ hosts: ['127.0.0.1'], app_port:18080, db_port:13306, payload_ready: false, elevated: false, version: '' })
const form = ref({ directory: '', storage: '', company: '', host: '127.0.0.1', port: 18080, db_mode: 'new', db_host: '127.0.0.1', db_port: 13306, db_name: 'gmpan', db_user: 'gmpan', db_password: '', admin: 'admin', password: '', firewall: true })
const confirmPassword = ref(''), checking = ref(false), starting = ref(false), checked = ref(false), loaded = ref(false), loadError = ref('')
const folderDialog = ref(false), folderLoading = ref(false), folderField = ref<'directory' | 'storage'>('directory'), folderPath = ref(''), folderError = ref('')
const folders = ref({ path:'', parent:'', items:[] as {name:string;path:string}[], selectable:false, truncated:false, missing:false })
const progress = ref({ operation:'install', running: false, done: false, percent: 0, steps: [] as string[], error: '', url: '', log: '', directory: '' })
const started = computed(() => progress.value.running || progress.value.done || !!progress.value.error)
const lan = computed(() => form.value.host !== '127.0.0.1')
let timer: ReturnType<typeof setTimeout> | undefined

async function api(path: string, data?: unknown) {
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 25000)
  try {
  const response = await fetch('/api/setup/' + path, { method: data === undefined ? 'GET' : 'POST', headers: { 'X-Setup-Token': token, ...(data === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: data === undefined ? undefined : JSON.stringify(data), cache: 'no-store', signal:controller.signal })
  const result = await response.json().catch(() => ({ message: '向导连接失效，请重新双击安装向导打开页面' }))
  if (!response.ok) throw new Error(result.message || '操作未完成')
  return result
  } catch(e) {
    if (controller.signal.aborted) throw new Error('操作等待超时，页面已恢复。请检查本机磁盘和服务状态后重试；尚未确认的安装请先查看进度，勿重复启动。')
    throw e
  } finally { clearTimeout(timeout) }
}
function fail(e: unknown) { ElMessage.error(e instanceof Error ? e.message : '操作未完成') }
function valid() {
  if (!form.value.company.trim()) throw new Error('请填写公司名称')
  const size = new TextEncoder().encode(form.value.password).length
  if (size < 10 || size > 72) throw new Error('超管密码需为 10～72 字节，建议使用字母、数字和符号')
  if (form.value.password !== confirmPassword.value) throw new Error('两次输入的超管密码不一致')
}
async function check() {
  checking.value = true; checked.value = false
  try { valid(); const reply = await api('check', form.value); checked.value = true; ElMessage.success(reply.message) } catch (e) { fail(e) } finally { checking.value = false }
}
async function install() {
  starting.value = true
  try { valid(); await api('install', form.value); form.value.password = ''; confirmPassword.value = ''; form.value.db_password = ''; await poll() } catch (e) { fail(e) } finally { starting.value = false }
}
async function poll() {
  try { progress.value = await api('progress'); if (progress.value.running) timer = setTimeout(poll, 1000) } catch (e) { fail(e) }
}
async function browse(field: 'directory' | 'storage') {
  folderField.value=field;folderDialog.value=true;await listFolders(form.value[field])
}
async function listFolders(path='') {
  folderLoading.value=true;folderError.value=''
  try { folders.value=await api('directories?path='+encodeURIComponent(path));folderPath.value=folders.value.path } catch(e) { folderError.value=e instanceof Error?e.message:'目录读取失败';folders.value.selectable=false } finally { folderLoading.value=false }
}
function chooseFolder(){form.value[folderField.value]=folders.value.path;checked.value=false;folderDialog.value=false}
async function close() { try { await api('exit', {}); loaded.value = false; loadError.value = '安装向导已关闭，可以关闭本页面。' } catch (e) { fail(e) } }
function changeHost() { form.value.port = lan.value ? 8443 : info.value.app_port; checked.value = false }
function changeDBMode() { form.value.db_port = form.value.db_mode === 'new' ? info.value.db_port : 3306; checked.value = false }
onMounted(async () => {
  try { const result = await api('info'); info.value = result; form.value.directory = result.directory; form.value.storage = result.storage; form.value.port = result.app_port; form.value.db_port = result.db_port; loaded.value = true } catch (e) { loadError.value = e instanceof Error ? e.message : '向导加载失败' }
})
onUnmounted(() => clearTimeout(timer))
</script>

<template>
  <main class="setup-shell">
    <header class="setup-header"><div class="setup-brand">G<span>M</span></div><div><div class="setup-eyebrow">GMPAN · LOCAL INSTALLATION</div><h1>图形部署向导</h1><p>在这台 Windows 电脑上，建立你的公司文件空间。</p></div><el-tag type="warning" effect="plain">本地测试版</el-tag></header>
    <el-alert v-if="loadError" :title="loadError" type="warning" :closable="false" show-icon />
    <template v-else-if="loaded">
      <div class="setup-status"><span :class="{ good: info.elevated }">{{ info.elevated ? '● 管理员权限已就绪' : '○ 请以管理员身份运行' }}</span><span :class="{ good: info.payload_ready }">{{ info.payload_ready ? '● 离线环境组件已找到' : '○ 缺少完整环境组件' }}</span><span>Go / Node.js 无需安装</span></div>
      <div v-if="!started" class="setup-mode"><el-button :type="mode==='install'?'primary':'default'" :disabled="checking||starting" @click="mode='install'">全新安装</el-button><el-button :type="mode==='uninstall'?'danger':'default'" :disabled="checking||starting" @click="mode='uninstall'">卸载 / 清理旧服务</el-button></div><el-steps v-if="mode==='install'" :active="progress.done ? 3 : started ? 2 : checked ? 1 : 0" finish-status="success" align-center><el-step title="填写参数"/><el-step title="环境检查"/><el-step title="安装与验证"/></el-steps>
      <div v-if="!started && mode==='install'" class="setup-layout">
        <el-form class="setup-form" label-position="top" :disabled="starting || checking" @input="checked = false" @submit.prevent="check">
          <section><h2><b>01</b> 公司与文件目录</h2><el-form-item label="公司名称"><el-input v-model="form.company" placeholder="请输入公司名称，稍后也可在后台调整" maxlength="100"/></el-form-item>
            <el-form-item label="站点部署目录（必须为空）"><el-input v-model="form.directory"><template #append><el-button @click="browse('directory')">选择目录</el-button></template></el-input><div class="setup-hint">程序、数据库、证书与日志保存在此目录。安装完成后不要移动或删除。</div></el-form-item>
            <el-form-item label="文件存储和读取目录"><el-input v-model="form.storage"><template #append><el-button @click="browse('storage')">选择目录</el-button></template></el-input><div class="setup-hint">可选择已有公司资料目录；必须与站点部署目录分开。已有 .gmpan 的目录不会接管。</div></el-form-item>
          </section>
          <section><h2><b>02</b> 访问方式与数据库</h2><div class="setup-two"><el-form-item label="访问地址"><el-select v-model="form.host" @change="changeHost"><el-option v-for="host in info.hosts" :key="host" :label="host === '127.0.0.1' ? '仅本机测试 · 127.0.0.1' : '公司内网 · ' + host" :value="host"/></el-select></el-form-item><el-form-item label="网站端口"><el-input-number v-model="form.port" :min="1024" :max="65535" controls-position="right"/></el-form-item></div>
            <el-alert v-if="lan" title="内网模式自动生成 HTTPS 证书。员工电脑首次访问需信任公开证书，安装完成后提供操作说明。" type="info" :closable="false"/>
            <el-checkbox v-if="lan" v-model="form.firewall">允许同子网电脑访问（仅 Windows 专用/域网络）</el-checkbox>
            <el-form-item label="数据库方式" class="setup-db-mode"><el-radio-group v-model="form.db_mode" @change="changeDBMode"><el-radio-button value="new">新装 MySQL 8.4 LTS</el-radio-button><el-radio-button value="existing">连接本机空数据库</el-radio-button></el-radio-group></el-form-item>
            <p v-if="form.db_mode === 'new'" class="setup-hint">使用独立服务、独立数据目录和随机强密码，账号自动生成；不会修改已有 MySQL。</p>
            <div class="setup-two"><el-form-item label="数据库端口"><el-input-number v-model="form.db_port" :min="1024" :max="65535" controls-position="right"/></el-form-item><el-form-item v-if="form.db_mode === 'existing'" label="专用空数据库名称"><el-input v-model="form.db_name"/></el-form-item></div>
            <template v-if="form.db_mode === 'existing'"><div class="setup-two"><el-form-item label="数据库专用账号"><el-input v-model="form.db_user" autocomplete="off"/></el-form-item><el-form-item label="数据库账号密码"><el-input v-model="form.db_password" type="password" show-password autocomplete="new-password"/></el-form-item></div><el-alert title="仅连接 127.0.0.1 的专用空库，账号需有该库建表权限；已有数据会阻止安装。已有数据库服务的开机自启动由原维护人员配置。" type="warning" :closable="false"/></template>
          </section>
          <section><h2><b>03</b> 创建超管</h2><el-form-item label="超管用户名"><el-input v-model="form.admin" autocomplete="username" placeholder="字母开头，3～32 位"/></el-form-item><div class="setup-two"><el-form-item label="超管密码"><el-input v-model="form.password" type="password" show-password autocomplete="new-password" placeholder="至少 10 位"/></el-form-item><el-form-item label="再次输入密码"><el-input v-model="confirmPassword" type="password" show-password autocomplete="new-password"/></el-form-item></div><p class="setup-hint">请妥善保存密码，向导不会在安装结果或日志中记录超管密码。</p></section>
          <div class="setup-actions"><el-button size="large" :loading="checking" :disabled="!info.payload_ready || !info.elevated" native-type="submit">检查环境</el-button><el-button type="primary" size="large" :loading="starting" :disabled="!checked || !info.payload_ready || !info.elevated" @click="install">开始安装</el-button></div>
          <el-alert v-if="checking" title="正在检查目录、端口和 Windows 服务。连接已有数据库时还会检查连接与空库；通常几秒内完成，超过 20 秒会提示超时，不会开始安装。" type="info" :closable="false" show-icon/>
        </el-form>
        <aside class="setup-aside"><div class="setup-card"><span class="setup-card-label">安装将自动完成</span><h3>一次配置，随系统启动</h3><ul><li>MySQL 8.4 独立数据库</li><li>数据表与超管初始化</li><li>ClamAV 与离线病毒库</li><li>内网 HTTPS 证书</li><li>低权限 Windows 服务</li><li>开机自启与故障重启</li><li>启动后的站点健康检查</li></ul></div><div class="setup-card light"><h3>你的已有资料会保留</h3><p>向导只用于新安装。已有站点、非空数据库和服务会阻止覆盖。</p><p>安装后在后台检查资料目录，再手动扫描导入公司空间。</p><p>短信配置仅在超管后台管理，新安装默认关闭。</p></div><p class="setup-footnote">{{ info.version }}<br/>本向导只在本机打开，安装参数不会发往外部服务器。</p></aside>
      </div>
      <SetupUninstall v-else-if="!started" :api="api" @started="poll"/><section v-else class="setup-progress"><div class="setup-progress-title"><div><span class="setup-eyebrow">{{ progress.done ? 'READY TO USE' : progress.error ? 'NEEDS ATTENTION' : 'INSTALLING' }}</span><h2>{{ progress.operation==='uninstall'?(progress.done?'卸载完成，数据已保留':progress.error?'卸载已停止':'正在卸载服务'):(progress.done?'安装完成，可以开始测试':progress.error?'安装已停止':'正在配置这台电脑') }}</h2></div><el-tag :type="progress.done ? 'success' : progress.error ? 'danger' : 'primary'">{{ progress.percent }}%</el-tag></div><el-progress :percentage="progress.percent" :status="progress.done ? 'success' : progress.error ? 'exception' : undefined" :stroke-width="10"/><ol class="setup-log"><li v-for="(line, index) in progress.steps" :key="index">{{ line }}</li></ol><el-alert v-if="progress.error" :title="progress.error" type="error" :closable="false" show-icon/><template v-if="progress.done && progress.operation!=='uninstall'"><div class="setup-result"><label>站点地址</label><a :href="progress.url" target="_blank" rel="noreferrer">{{ progress.url }}</a><label>部署目录</label><span>{{ progress.directory }}</span></div><el-alert title="请先登录并测试上传、预览和下载。完成一次目标电脑重启测试后，再正式投入使用；不要删除部署目录。" type="info" :closable="false"/><p>内网 HTTPS 首次访问需按部署说明信任 certs/server.crt，不能分发私钥。<br/>安装结果和数据库维护凭据保存在部署目录中，请由管理员妥善备份。</p><el-button type="primary" size="large" tag="a" :href="progress.url" target="_blank" rel="noreferrer">打开 GmPan</el-button></template><p v-if="progress.done && progress.operation==='uninstall'">请查看上述结果。数据库和文件仍在原目录；若提示等待删除，请关闭服务管理器和属性窗口，必要时重启电脑。不要删除保留数据来强行通过新安装检查。</p><p v-if="progress.log" class="setup-hint">安装日志：{{ progress.log }}</p><el-button v-if="!progress.running" @click="close">关闭安装向导</el-button><p v-else class="setup-hint">{{ progress.operation==='uninstall'?'正在停止和移除服务，请保持向导运行。':'解压和数据库初始化可能需要几分钟，请保持本向导运行。' }}</p></section>
      <footer class="setup-footer">© 长沙感梦科技有限公司 · GmPan <el-button v-if="!started" text @click="close">退出向导</el-button></footer>
      <el-dialog v-model="folderDialog" :title="folderField==='directory'?'选择站点部署目录':'选择文件存储和读取目录'" width="640px" :close-on-click-modal="false">
        <p class="setup-hint">直接在本页面选择这台部署电脑上的目录；也可以关闭窗口，在表单中填写尚未创建的新目录。</p>
        <div class="setup-folder-path"><el-input v-model="folderPath" placeholder="本机完整目录路径" @keyup.enter="listFolders(folderPath)"/><el-button :loading="folderLoading" @click="listFolders(folderPath)">浏览</el-button></div>
        <div class="setup-folder-actions"><el-button :disabled="folderLoading" @click="listFolders()">本机磁盘</el-button><el-button :disabled="folderLoading || !folders.path" @click="listFolders(folders.parent)">上一级</el-button><span>{{ folders.path || '本机磁盘' }}</span></div>
        <el-alert v-if="folderError" :title="folderError" type="error" :closable="false"/>
        <el-alert v-else-if="folders.missing" title="输入目录尚不存在，当前显示最近的已有上级目录；原表单路径尚未更改。" type="info" :closable="false"/>
        <div v-loading="folderLoading" class="setup-folder-list"><el-button v-for="folder in folders.items" :key="folder.path" text @click="listFolders(folder.path)">📁 {{ folder.name }}</el-button><el-empty v-if="!folderLoading && !folders.items.length && !folderError" description="此目录没有可显示的子目录" :image-size="54"/></div>
        <p v-if="folders.truncated" class="setup-hint">目录较多，仅显示部分内容；可在上方输入完整路径继续浏览。</p>
        <template #footer><el-button @click="folderDialog=false">取消</el-button><el-button type="primary" :disabled="folderLoading || !folders.selectable" @click="chooseFolder">选择当前目录</el-button></template>
      </el-dialog>
    </template>
    <div v-else-if="!loadError" class="setup-loading">正在读取本机部署环境…</div>
  </main>
</template>

<style>
*{box-sizing:border-box}body{margin:0;background:#f2f5fa;font-family:Inter,"Microsoft YaHei",sans-serif;color:#1d2b43}.setup-shell{max-width:1140px;margin:auto;padding:38px 36px}.setup-header{display:flex;align-items:center;gap:18px;margin-bottom:24px}.setup-header>.el-tag{margin-left:auto}.setup-brand{height:64px;width:64px;background:#183e80;color:white;border-radius:18px;display:grid;place-content:center;font-size:22px;font-weight:800;letter-spacing:-4px;display:flex;align-items:center;padding-right:4px}.setup-brand span{color:#72c7ef}.setup-eyebrow{font-size:11px;letter-spacing:2px;color:#6682aa;font-weight:700}.setup-header h1{margin:5px 0;font-size:27px}.setup-header p{margin:0;color:#758398;font-size:13px}.setup-status{display:flex;gap:28px;flex-wrap:wrap;padding:16px 20px;border:1px solid #dce5f0;border-radius:12px;background:white;margin-bottom:30px;font-size:12px;color:#98732e}.setup-status .good{color:#278267}.setup-status span:last-child{margin-left:auto;color:#758398}.setup-layout{display:grid;grid-template-columns:minmax(0,1fr) 270px;gap:24px;margin-top:30px}.setup-form,.setup-progress{padding:28px 32px;background:white;border:1px solid #e1e8f1;border-radius:16px;box-shadow:0 8px 28px #183e8005}.setup-form section+section{border-top:1px solid #edf0f5;padding-top:20px;margin-top:26px}.setup-form h2{font-size:16px;margin:0 0 24px;display:flex;gap:12px;align-items:center}.setup-form h2 b{font-size:12px;border-radius:7px;background:#eaf1ff;color:#315b9c;padding:7px}.setup-form .el-form-item{margin-bottom:18px}.setup-form .el-form-item__label{font-size:13px;color:#475772}.setup-two{display:grid;grid-template-columns:1fr 1fr;gap:18px}.setup-two .el-input-number,.setup-two .el-select{width:100%}.setup-hint{font-size:12px;line-height:1.8;color:#8993a3;margin:7px 0 0}.setup-db-mode{margin-top:20px}.setup-actions{display:flex;justify-content:flex-end;gap:8px;padding-top:26px;border-top:1px solid #edf0f5;margin-top:24px}.setup-card{border-radius:16px;padding:25px 23px;background:#183e80;color:#e4edfa;margin-bottom:18px}.setup-card-label{font-size:11px;letter-spacing:1px;color:#8cbaed}.setup-card h3{font-size:17px;line-height:1.5;margin:12px 0 20px}.setup-card ul{padding-left:18px;margin:0;font-size:13px;line-height:2.8}.setup-card.light{background:#e9eef6;color:#536780}.setup-card.light h3{color:#314a6d;font-size:15px;margin-top:0;margin-bottom:14px}.setup-card p{font-size:12px;line-height:1.9}.setup-footnote{font-size:11px;line-height:1.9;color:#9aa5b4;padding:0 6px}.setup-progress{margin:30px auto;max-width:850px}.setup-progress-title{display:flex;justify-content:space-between;align-items:center}.setup-progress h2{font-size:22px}.setup-log{padding:18px 0 18px 25px;color:#576b84;line-height:2.4;font-size:13px}.setup-progress p{color:#758398;font-size:13px;line-height:1.9}.setup-result{display:grid;grid-template-columns:90px 1fr;gap:15px;margin:24px 0;padding:20px;background:#f1f5fb;border-radius:10px;font-size:13px;overflow-wrap:anywhere}.setup-result label{color:#8594a7}.setup-result a{color:#315f9d}.setup-footer{display:flex;align-items:center;justify-content:center;gap:14px;font-size:11px;color:#9aa5b4;margin-top:28px}.setup-loading{text-align:center;margin:80px;color:#758398}.setup-shell .el-alert__title{font-size:12px;line-height:1.8}.setup-shell .el-steps{max-width:850px;margin:auto}.setup-form .el-checkbox{white-space:normal;height:auto;line-height:1.8;margin-top:15px}.setup-form .el-checkbox__label{white-space:normal;font-size:12px}@media(max-width:850px){.setup-shell{padding:22px 16px}.setup-layout{grid-template-columns:1fr}.setup-aside{display:none}.setup-header h1{font-size:22px}.setup-form,.setup-progress{padding:22px}.setup-status{gap:12px}.setup-status span:last-child{margin-left:0}}@media(max-width:520px){.setup-two{grid-template-columns:1fr;gap:0}.setup-header>.el-tag{display:none}.setup-header p{font-size:11px}.setup-status{font-size:11px}.setup-brand{width:50px;height:50px}.setup-result{grid-template-columns:1fr;gap:8px}.setup-form .el-radio-button__inner{padding:9px}.setup-shell .el-step__title{font-size:13px}}
.setup-folder-path,.setup-folder-actions{display:flex;gap:10px;align-items:center;margin:14px 0}.setup-folder-actions span{font-size:12px;color:#758398;overflow-wrap:anywhere;min-width:0}.setup-folder-list{height:260px;overflow:auto;border:1px solid #e1e8f1;border-radius:8px;padding:8px}.setup-folder-list>.el-button{display:flex;justify-content:flex-start;margin:0;width:100%;min-height:36px;white-space:normal;height:auto;text-align:left}.setup-actions+.el-alert{margin-top:14px}
.setup-mode{display:flex;gap:12px;justify-content:center;margin-bottom:26px}
</style>
