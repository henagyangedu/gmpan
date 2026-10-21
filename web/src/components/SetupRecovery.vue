<script setup lang="ts">
import { validPassword, passwordRequirement } from '../password'
import { ref, watch } from 'vue'

const props = defineProps<{ api: (path: string, data?: unknown) => Promise<any>; directory: string; elevated: boolean }>()
const emit = defineEmits<{ 'update:directory': [value: string]; browse: []; busy: [value: boolean] }>()
type Site = { directory: string; database: string; ticket: string; accounts: { username: string; name: string }[] }
const site = ref<Site>(), username = ref(''), password = ref(''), confirmPassword = ref('')
const busy = ref(false), confirmed = ref(false), error = ref(''), success = ref('')
watch(() => props.directory, () => { site.value = undefined; confirmed.value = false; error.value = ''; success.value = ''; password.value = ''; confirmPassword.value = '' })
function setBusy(value: boolean) { busy.value = value; emit('busy', value) }
async function load() {
  setBusy(true); site.value = undefined; error.value = ''; success.value = ''; confirmed.value = false; password.value = ''; confirmPassword.value = ''
  try {
    site.value = await props.api('recovery-info', { directory: props.directory })
    username.value = site.value!.accounts[0]!.username
  } catch (e: any) { error.value = e.message || '读取失败，请核对原站点部署目录' }
  finally { setBusy(false) }
}
async function reset() {
  error.value = ''; success.value = ''
  if (!validPassword(password.value)) { error.value = '新' + passwordRequirement; return }
  if (password.value !== confirmPassword.value) { error.value = '两次输入的新密码不一致'; return }
  if (!site.value || !confirmed.value) return
  setBusy(true)
  try {
    const result = await props.api('recovery-reset', { ticket: site.value.ticket, username: username.value, password: password.value, confirm_password: confirmPassword.value, confirmed: confirmed.value })
    success.value = result.message; site.value = undefined; confirmed.value = false
  } catch (e: any) { error.value = e.message || '结果尚未确认，请先使用新密码登录原站点，再决定是否重试' }
  finally { password.value = ''; confirmPassword.value = ''; setBusy(false) }
}
</script>

<template>
  <section class="setup-progress setup-recovery">
    <h2>重置超管密码</h2>
    <el-alert title="在原站点设置新密码" description="无需旧密码和短信。仅限部署电脑的 Windows 管理员使用，不重新安装、不清空文件或数据库。请保持原 MySQL 服务运行。" type="info" :closable="false" show-icon/>
    <el-alert v-if="!elevated" title="请关闭向导，右键以管理员身份运行后再操作" type="warning" :closable="false"/>
    <el-form label-position="top" :disabled="busy || !elevated" @submit.prevent="reset">
      <el-form-item label="原站点部署目录（内含正在使用的 config.json）">
        <el-input :model-value="directory" placeholder="例如 D:\GmPan，选择安装后的程序目录" @update:model-value="emit('update:directory', $event)"><template #append><el-button @click="emit('browse')">选择目录</el-button></template></el-input>
        <p class="setup-hint">请选择已安装站点的目录，不能选择下载包解压目录或文件资料目录。也可以直接粘贴完整目录路径。</p>
      </el-form-item>
      <el-button :loading="busy" :disabled="!directory" @click="load">读取原站点</el-button>
      <template v-if="site">
        <div class="setup-result"><label>配置目录</label><span>{{ site.directory }}</span><label>数据库</label><span>{{ site.database }}</span></div>
        <el-form-item label="要恢复的超管账号"><el-select v-model="username" autocomplete="username"><el-option v-for="account in site.accounts" :key="account.username" :value="account.username" :label="account.username + ' · ' + account.name"/></el-select></el-form-item>
        <div class="setup-two">
          <el-form-item label="新密码"><el-input v-model="password" name="recovery-new-password" type="password" show-password autocomplete="new-password" placeholder="至少 8 位，包含字母和数字"/></el-form-item>
          <el-form-item label="再次输入新密码"><el-input v-model="confirmPassword" name="recovery-confirm-password" type="password" show-password autocomplete="new-password"/></el-form-item>
        </div>
        <p class="setup-hint">浏览器生成密码时，请确认两栏一致并妥善保存。恢复工具不会显示旧密码，也不会在结果或日志中记录新密码。</p>
        <el-checkbox v-model="confirmed">我已核对原站点和账号，确认重置密码并使该账号的旧登录失效</el-checkbox>
        <div class="setup-actions"><el-button type="primary" native-type="submit" :loading="busy" :disabled="!confirmed || !password || !confirmPassword">确认重置超管密码</el-button></div>
      </template>
    </el-form>
    <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon/>
    <el-alert v-if="success" :title="success" type="success" :closable="false" show-icon/>
    <p v-if="success">在原网站登录页输入上面选择的用户名和新密码；浏览器若仍自动填入旧密码，请先清空再手动输入。无需重装或重启站点。</p>
    <p>此功能可单独使用，无需完整环境组件。账号权限、启用状态及其他员工的密码保持原样；恢复记录保存在原站点的操作日志中。</p>
  </section>
</template>

<style scoped>
.setup-recovery .el-form{margin-top:24px}.setup-recovery .el-select{width:100%}.setup-recovery .el-alert{margin-top:16px}
</style>
