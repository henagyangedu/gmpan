<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { OfficeBuilding, UploadFilled, Refresh, Check } from '@element-plus/icons-vue'
const props = defineProps<{ api: (path: string, method?: string, body?: any) => Promise<any>; logoUrl: string }>()
const emit = defineEmits<{ updated: [] }>()
const loading = ref(true), saving = ref(false), logoBusy = ref(false), smsSaving = ref(false)
const tab = ref('site'), logoInput = ref<HTMLInputElement>()
const site = ref({ company_name: '', company_short_name: '', copyright: '', copyright_url: '', default_theme: 'explorer', storage_root: '', storage_warning_percent: 10, revision: 0 })
const runtime = ref({ active_storage_root: '', deployment_dir: '', migration_required: false, previous_data_dir: '', previous_source_dir: '', restart_required: false, has_logo: false })
const sms = ref({ enabled: false, base_url: '', login_scene: '', has_app_key: false, has_app_secret: false, revision: 0 })
const credentials = ref({ app_key: '', app_secret: '', clear_credentials: false })
const themes = [{ id: 'explorer', name: '目录工作台', note: 'A · 电脑目录与紧凑列表' }, { id: 'library', name: '影像资料库', note: 'B · 视频卡片与资源浏览' }, { id: 'classic', name: '清爽协作', note: '原有绿色工作空间' }]
function error(e: any) { ElMessage.error(e.message || '操作未完成') }
async function load() { loading.value = true; try { const [s, m] = await Promise.all([props.api('/admin/site-settings'), props.api('/admin/sms-settings')]); site.value = s.settings; runtime.value = s; sms.value = m; credentials.value = { app_key: '', app_secret: '', clear_credentials: false } } catch (e) { error(e) } finally { loading.value = false } }
async function save() { saving.value = true; try { const result = await props.api('/admin/site-settings', 'PUT', site.value); site.value = result.settings; runtime.value = result; emit('updated'); ElMessage.success(result.restart_required ? '站点配置已保存，目录配置将在重启服务后生效' : '站点配置已生效') } catch (e) { error(e) } finally { saving.value = false } }
async function changeLogo(event: Event) { const input = event.target as HTMLInputElement, file = input.files?.[0]; input.value = ''; if (!file) return; if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > 2 * 1024 * 1024) { ElMessage.warning('请选择 2 MB 以内的 PNG 或 JPEG 图片'); return }; logoBusy.value = true; try { const result = await props.api('/admin/site-logo', 'PUT', file); site.value.revision = result.settings.revision; runtime.value = result; emit('updated'); ElMessage.success('公司 Logo 已更新') } catch (e) { error(e) } finally { logoBusy.value = false } }
async function removeLogo() { logoBusy.value = true; try { const result = await props.api('/admin/site-logo', 'DELETE'); site.value.revision = result.settings.revision; runtime.value = result; emit('updated'); ElMessage.success('已恢复默认图标') } catch (e) { error(e) } finally { logoBusy.value = false } }
async function saveSMS() { smsSaving.value = true; try { const { has_app_key, has_app_secret, ...fields } = sms.value; sms.value = await props.api('/admin/sms-settings', 'PUT', { ...fields, ...credentials.value }); credentials.value = { app_key: '', app_secret: '', clear_credentials: false }; emit('updated'); ElMessage.success('短信配置已生效') } catch (e) { error(e) } finally { smsSaving.value = false } }
onMounted(load)
</script>
<template>
  <section v-loading="loading" class="site-settings files-panel">
    <div class="section-title"><h3>站点配置</h3><el-button :icon="Refresh" @click="load">重新加载</el-button></div>
    <el-tabs v-model="tab" class="settings-tabs"><el-tab-pane label="站点与目录" name="site"/><el-tab-pane label="短信配置" name="sms"/></el-tabs>
    <el-form v-if="tab === 'site'" label-position="top" class="settings-form" @submit.prevent="save">
      <h3>公司标识</h3><div class="form-two"><el-form-item label="公司名称"><el-input v-model="site.company_name" maxlength="100" show-word-limit /></el-form-item><el-form-item label="公司简称"><el-input v-model="site.company_short_name" maxlength="24" show-word-limit /></el-form-item></div>
      <el-form-item label="公司 Logo"><div class="logo-settings"><div class="logo-preview"><img v-if="logoUrl" :src="logoUrl" alt="公司 Logo"><el-icon v-else :size="32"><OfficeBuilding/></el-icon></div><div><el-button :icon="UploadFilled" :loading="logoBusy" @click="logoInput?.click()">上传 Logo</el-button><el-button v-if="runtime.has_logo" :disabled="logoBusy" @click="removeLogo">恢复默认图标</el-button><p class="subtle">PNG / JPEG，最大 2 MB，宽高均不超过 1024 像素；上传后立即生效。</p></div></div><input ref="logoInput" type="file" accept="image/png,image/jpeg" hidden @change="changeLogo"></el-form-item>
      <h3>默认主题</h3><div class="theme-options"><button v-for="t in themes" :key="t.id" type="button" :class="['theme-option', t.id, { selected: site.default_theme === t.id }]" :aria-pressed="site.default_theme === t.id" @click="site.default_theme = t.id"><span class="theme-swatch"><i/><i/><i/></span><strong>{{ t.name }}<el-icon v-if="site.default_theme === t.id"><Check/></el-icon></strong><small>{{ t.note }}</small></button></div><p class="subtle">员工可在页面右上角选择自己的主题，或跟随站点默认。</p>
      <h3>文件与部署目录</h3>
      <el-alert v-if="runtime.restart_required" type="warning" title="文件目录已保存，等待重启服务生效" :closable="false" show-icon/>
      <el-alert v-if="runtime.migration_required" type="warning" title="旧目录配置等待统一迁移" :closable="false" show-icon><p>原上传目录：{{ runtime.previous_data_dir }}<br>原公司资料目录：{{ runtime.previous_source_dir || '未配置' }}<br>已有文件时请先备份、停服并执行目录迁移；迁移完成前仍从原路径读取。</p></el-alert>
      <el-form-item label="文件存储和读取目录"><el-input v-model="site.storage_root" placeholder="例如 D:\公司资料"/><div class="subtle path-description">当前生效：{{ runtime.active_storage_root || '尚未统一，保持原目录' }}<br>原有公司资料在此目录扫描、预览和下载。系统上传的公司、部门、个人文件及分片在内部 .gmpan 目录隔离保存，公开扫描会跳过该目录。</div></el-form-item>
      <el-form-item label="站点部署目录"><el-input :model-value="runtime.deployment_dir" readonly/><div class="subtle path-description">正在运行的程序所在目录，只读显示。程序、配置、证书和日志按部署手册放置；移动站点需停服操作。</div></el-form-item>
      <el-form-item label="剩余空间告警阈值"><el-input-number v-model="site.storage_warning_percent" :min="1" :max="99" :precision="0"/><span class="threshold-unit">%</span><div class="subtle path-description">默认 10%。文件目录所在磁盘的可用空间低于此比例时，在系统页面提醒维护；每分钟更新一次。</div></el-form-item>
      <p class="settings-note">这里填写服务器上的路径。已有文件或索引时不能直接换到另一目录，需备份后停服迁移；迁移会复制并校验文件，保留原文件及权限、备注。文件目录不能与站点程序、配置、证书或日志目录重叠。</p>
      <h3>底部版权</h3><el-form-item label="版权说明"><el-input v-model="site.copyright" maxlength="200" placeholder="长沙感梦科技有限公司 版权所有"/></el-form-item><el-form-item label="版权关联网址"><el-input v-model="site.copyright_url" placeholder="https://www.gmkj.cc/"/></el-form-item><div class="settings-actions"><el-button type="primary" native-type="submit" :loading="saving">保存站点配置</el-button></div>
    </el-form>
    <el-form v-else label-position="top" class="settings-form" @submit.prevent="saveSMS"><h3>短信登录</h3><el-form-item label="启用短信验证码登录"><el-switch v-model="sms.enabled" active-text="启用" inactive-text="关闭"/></el-form-item><el-form-item label="短信平台接口基址"><el-input v-model="sms.base_url" placeholder="https://sms.gmwang.com.cn/api/v1"/></el-form-item><div class="form-two"><el-form-item label="AppKey"><el-input v-model="credentials.app_key" type="password" show-password autocomplete="new-password" :placeholder="sms.has_app_key ? '已配置，留空保留原值' : '填写平台分配的 AppKey'"/></el-form-item><el-form-item label="AppSecret"><el-input v-model="credentials.app_secret" type="password" show-password autocomplete="new-password" :placeholder="sms.has_app_secret ? '已配置，留空保留原值' : '填写平台分配的 AppSecret'"/></el-form-item></div><el-form-item label="登录验证码场景"><el-input v-model="sms.login_scene" maxlength="64" placeholder="填写短信平台已开通的场景标识"/></el-form-item><el-checkbox v-model="credentials.clear_credentials">清除已保存的 AppKey 和 AppSecret（需关闭短信登录）</el-checkbox><p class="settings-note">凭证仅保存在服务端，保存后不回显。请使用独立项目凭证，并在短信平台配置场景、模板、额度与出口 IP 白名单。保存配置不会发送短信。</p><p class="subtle">关闭短信登录后，员工仍可使用用户名或绑定手机号加密码登录。</p><div class="settings-actions"><el-button type="primary" native-type="submit" :loading="smsSaving">保存短信配置</el-button></div></el-form>
  </section>
</template>
