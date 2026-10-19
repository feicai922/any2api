<template>
  <div v-if="loginPage" class="login-screen">
    <el-card class="login-card" shadow="never">
      <template #header>
        <div class="login-heading">
          <h1>管理员登录</h1>
          <p>输入统一访问密码后可启动、停止项目，并进入各项目的账号控制台。</p>
        </div>
      </template>
      <el-form class="login-form" :model="loginForm" label-position="top" @submit.prevent="submitLogin">
        <el-form-item label="访问密码">
          <el-input
            v-model="loginForm.password"
            type="password"
            show-password
            autocomplete="current-password"
            placeholder="请输入统一访问密码"
            autofocus
            @keyup.enter="submitLogin"
          />
        </el-form-item>
        <el-button class="full-button login-submit" size="large" type="primary" native-type="submit" :loading="loginLoading">
          登录并进入工作台
        </el-button>
      </el-form>
      <p v-if="loginError" class="login-error" role="alert">{{ loginError }}</p>
      <el-divider />
      <p class="footnote">公网当前使用 HTTP。密码和会话可能在传输链路中被窃听，建议后续启用 HTTPS。</p>
    </el-card>
  </div>

  <div v-else class="app-shell">
    <aside class="sidebar">
      <div class="brand-block">
        <div class="brand-title">反代项目工作台</div>
        <div class="brand-subtitle">API Gateway · 五项目统一管理</div>
      </div>

      <div class="nav-section">
        <div class="nav-label">项目</div>
        <button
          v-for="project in projects"
          :key="project.name"
          class="project-button"
          :class="{ active: project.name === selectedName }"
          type="button"
          @click="selectProject(project.name)"
        >
          <span class="status-dot" :class="statusClass(project)"></span>
          <span class="project-name">{{ project.name }}</span>
          <span class="project-state">{{ statusLabel(project) }}</span>
        </button>
      </div>

      <div class="nav-section">
        <div class="nav-label">帮助</div>
        <button class="nav-button" :class="{ active: page === 'docs' }" type="button" @click="openPage('docs')">
          文档说明
        </button>
        <button
          v-if="manage"
          class="nav-button"
          type="button"
          @click="startAll"
        >
          启动全部项目
        </button>
      </div>

      <div class="nav-section nav-bottom">
        <div class="nav-label">账户与管理</div>
        <button class="nav-button" type="button" @click="openAccounts">
          账号登录说明
        </button>
        <span v-if="isLocal" class="local-label">本机管理</span>
        <button v-else class="nav-button" type="button" @click="logout">退出管理</button>
      </div>
    </aside>

    <main class="main-content">
      <template v-if="page === 'docs'">
        <header class="page-header">
          <div>
            <div class="eyebrow">文档说明</div>
            <h1>统一 API 与项目管理</h1>
            <p>五个反代项目共用一个入口；左侧切换项目，右侧查看状态和调用方式。</p>
          </div>
          <el-button @click="openPage('workspace')">返回工作台</el-button>
        </header>

        <el-card class="section-card" shadow="never">
          <template #header><h2>统一入口</h2></template>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="本机管理">{{ localAdminURL }}</el-descriptions-item>
            <el-descriptions-item label="管理入口">{{ apiBase }}/admin</el-descriptions-item>
            <el-descriptions-item label="API 入口">{{ apiBase }}</el-descriptions-item>
            <el-descriptions-item label="默认项目">{{ defaultProject }}</el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>项目与端口</h2></template>
          <el-table :data="projects" stripe>
            <el-table-column prop="name" label="项目" min-width="110" />
            <el-table-column prop="public_base" label="项目 Base URL" min-width="260" />
            <el-table-column prop="upstream" label="本地上游" min-width="190" />
            <el-table-column prop="directory" label="目录" min-width="270" show-overflow-tooltip />
          </el-table>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>协议能力矩阵</h2></template>
          <el-table :data="projects" stripe>
            <el-table-column prop="name" label="项目" min-width="110" />
            <el-table-column label="Chat Completions" min-width="165">
              <template #default="{ row }">
                <el-tag :type="protocol(row, 'chat') ? 'success' : 'info'" effect="plain">
                  {{ protocol(row, 'chat') ? '原生' : '转换' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="Responses" min-width="150">
              <template #default="{ row }">
                <el-tag :type="protocol(row, 'responses') ? 'success' : 'info'" effect="plain">
                  {{ protocol(row, 'responses') ? '原生' : '转换' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column label="Anthropic Messages" min-width="180">
              <template #default="{ row }">
                <el-tag :type="protocol(row, 'anthropic') ? 'success' : 'info'" effect="plain">
                  {{ protocol(row, 'anthropic') ? '原生' : '转换' }}
                </el-tag>
              </template>
            </el-table-column>
          </el-table>
          <p class="body-copy">Chat 是所有项目的转换回退协议。Responses 与 Anthropic 未原生支持时由网关转换成 Chat；流式 SSE、工具调用和图片输入最终受各项目上游能力限制。</p>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>调用示例</h2></template>
          <p class="body-copy">API 请求把统一访问密码作为 Bearer Token；管理员登录会话不能替代 API Key。</p>
          <h3>Chat Completions</h3>
          <pre class="code-block">POST {{ apiBase }}/workbuddy/v1/chat/completions
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json

{"model":"cn:glm-5.2","messages":[{"role":"user","content":"你好"}]}</pre>
          <h3>Responses</h3>
          <pre class="code-block">POST {{ apiBase }}/qoder/v1/responses
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json

{"model":"qmodel","input":"你好"}</pre>
          <h3>Anthropic Messages</h3>
          <pre class="code-block">POST {{ apiBase }}/qoder/v1/messages
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json
anthropic-version: 2023-06-01

{"model":"qmodel","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}</pre>
        </el-card>

        <el-card id="accounts" class="section-card" shadow="never">
          <template #header><h2>账号登录与控制台</h2></template>
          <p class="body-copy">控制台不嵌入主面板。点击“打开控制台”会在新窗口进入项目自己的登录页面；工作台不读取、不保存账号密码或 API Key。</p>
          <el-table :data="projects" stripe>
            <el-table-column prop="name" label="项目" min-width="110" />
            <el-table-column label="状态" min-width="100">
              <template #default="{ row }">
                <el-tag :type="tagType(row)" effect="plain">{{ statusLabel(row) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="login_hint" label="登录说明" min-width="300" />
            <el-table-column label="操作" width="130">
              <template #default="{ row }">
                <el-button
                  v-if="row.console_url"
                  link
                  type="primary"
                  tag="a"
                  :href="row.console_url"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  打开控制台
                </el-button>
                <span v-else>不可用</span>
              </template>
            </el-table-column>
          </el-table>
          <h3>常见现象</h3>
          <ul class="plain-list">
            <li><strong>401：</strong>管理员会话过期、面板密码或项目 API Key 不正确。</li>
            <li><strong>Qoder 返回 502：</strong>通常尚未完成 OAuth/PAT，8963 Bridge 未启动。</li>
            <li><strong>控制台白屏或跳登录：</strong>先确认项目已运行，再重新打开控制台。</li>
            <li><strong>账号状态：</strong>工作台只探测服务健康度，无法可靠读取第三方账号登录状态。</li>
          </ul>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>启动与管理</h2></template>
          <p class="body-copy">网关启动时会自动拉起五个项目，已运行项目会跳过；依赖未就绪的项目会显示原因。</p>
          <pre class="code-block"># 检查并安装依赖（可重复执行）
.\\scripts\\install.ps1

# 仅检查依赖
.\\scripts\\install.ps1 -CheckOnly

# 构建并启动网关（会提示输入管理密码）
.\\scripts\\start.ps1 -Build

# 查看状态 / 重启
.\\scripts\\control.ps1 status
.\\scripts\\control.ps1 restart</pre>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>安全与内网穿透</h2></template>
          <ul class="plain-list">
            <li>公网只映射网关 7860；管理端口 7862 只监听回环地址，不加入 frp。</li>
            <li>公网启停和项目控制台都要求管理员登录；未登录请求返回 401 或跳转登录页。</li>
            <li>管理员会话使用 HttpOnly、SameSite=Strict Cookie，有效期 12 小时；连续登录失败会短暂锁定。注意：当前设置的是 Strict，而非前端展示层的弱限制。</li>
            <li>页面不显示、不保存项目 API Key；统一凭据只在启动进程时通过环境变量注入。</li>
            <li>当前公网是 HTTP，密码与会话可能被链路窃听；建议后续通过 HTTPS 反向代理访问。</li>
          </ul>
        </el-card>

        <el-card class="section-card" shadow="never">
          <template #header><h2>健康检查</h2></template>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="网关自身">GET /healthz</el-descriptions-item>
            <el-descriptions-item label="项目别名">GET /&lt;project&gt;/health 或 /&lt;project&gt;/healthz</el-descriptions-item>
            <el-descriptions-item label="示例">{{ apiBase }}/workbuddy/health</el-descriptions-item>
          </el-descriptions>
        </el-card>
      </template>

      <template v-else>
        <header class="page-header">
          <div>
            <div class="eyebrow">工作台</div>
            <h1>{{ selectedProject ? selectedProject.name : '未找到项目' }}</h1>
            <p v-if="selectedProject">{{ selectedProject.description }}</p>
            <p v-else>请从左侧选择一个项目。</p>
          </div>
          <div v-if="selectedProject" class="header-actions">
            <el-button
              v-if="selectedProject.console_url"
              type="primary"
              tag="a"
              :href="selectedProject.console_url"
              target="_blank"
              rel="noopener noreferrer"
            >
              打开控制台
            </el-button>
            <el-button
              type="primary"
              plain
              :disabled="!canStart(selectedProject)"
              :loading="actionLoading === selectedProject.name + ':start'"
              @click="projectAction(selectedProject.name, 'start')"
            >
              启动
            </el-button>
            <el-button
              type="danger"
              plain
              :disabled="!canStop(selectedProject)"
              :loading="actionLoading === selectedProject.name + ':stop'"
              @click="projectAction(selectedProject.name, 'stop')"
            >
              停止
            </el-button>
          </div>
        </header>

        <section v-if="selectedProject" class="stat-grid">
          <el-card shadow="never" class="stat-card">
            <div class="stat-label">运行状态</div>
            <div class="stat-value">{{ statusLabel(selectedProject) }}</div>
            <el-tag :type="tagType(selectedProject)" effect="plain">{{ selectedProject.detail }}</el-tag>
          </el-card>
          <el-card shadow="never" class="stat-card">
            <div class="stat-label">进程 PID</div>
            <div class="stat-value mono">{{ selectedProject.pid || '—' }}</div>
            <span class="stat-note">{{ selectedProject.started_at || '尚未由工作台启动' }}</span>
          </el-card>
          <el-card shadow="never" class="stat-card">
            <div class="stat-label">依赖</div>
            <div class="stat-value">{{ selectedProject.installed ? '已就绪' : '未安装' }}</div>
            <span class="stat-note">{{ selectedProject.install_hint || '启动所需依赖已检查' }}</span>
          </el-card>
        </section>

        <el-card v-if="selectedProject" class="section-card" shadow="never">
          <template #header><h2>接口信息</h2></template>
          <el-descriptions :column="1" border>
            <el-descriptions-item label="公网 Base URL">
              <span class="mono">{{ selectedProject.public_base }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="本地上游">
              <span class="mono">{{ selectedProject.upstream }}</span>
            </el-descriptions-item>
            <el-descriptions-item label="项目目录">
              <span class="mono">{{ selectedProject.directory }}</span>
            </el-descriptions-item>
            <el-descriptions-item v-if="selectedProject.console_url" label="控制台入口">
              <span class="mono">{{ selectedProject.console_url }}</span>
            </el-descriptions-item>
            <el-descriptions-item v-if="selectedProject.console_direct" label="后端直连">
              <span class="mono">{{ selectedProject.console_direct }}</span>
            </el-descriptions-item>
            <el-descriptions-item v-if="selectedProject.last_error" label="最近错误">
              {{ selectedProject.last_error }}
            </el-descriptions-item>
          </el-descriptions>
        </el-card>

        <el-card v-if="selectedProject" class="section-card" shadow="never">
          <template #header><h2>协议能力</h2></template>
          <div class="protocol-list">
            <div v-for="item in selectedProject.protocols" :key="item.name" class="protocol-row">
              <span>{{ protocolTitle(item.name) }}</span>
              <el-tag :type="item.native ? 'success' : 'info'" effect="plain">
                {{ item.native ? '原生' : '转换' }}
              </el-tag>
            </div>
          </div>
        </el-card>

        <el-card v-if="manage" id="loginCenter" class="section-card" shadow="never">
          <template #header><h2>账号登录中心</h2></template>
          <p class="body-copy">每个项目使用自己的账号体系。工作台只提供独立窗口入口，不读取、不显示也不保存账号密码或 API Key。</p>
          <el-table :data="projects" stripe>
            <el-table-column prop="name" label="项目" min-width="110" />
            <el-table-column label="服务" min-width="140">
              <template #default="{ row }">
                <el-tag :type="tagType(row)" effect="plain">{{ statusLabel(row) }}</el-tag>
                <div class="table-note">{{ row.detail }}</div>
              </template>
            </el-table-column>
            <el-table-column prop="login_hint" label="登录方式 / 当前提示" min-width="320" />
            <el-table-column label="操作" width="130">
              <template #default="{ row }">
                <el-button
                  v-if="row.console_url"
                  link
                  type="primary"
                  tag="a"
                  :href="row.console_url"
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  打开控制台
                </el-button>
                <span v-else>不可用</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>

        <el-card v-if="selectedProject" class="section-card" shadow="never">
          <template #header><h2>调用示例</h2></template>
          <pre class="code-block">POST {{ selectedProject.public_base }}/chat/completions
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json

{"model":"cn:glm-5.2","messages":[{"role":"user","content":"你好"}]}</pre>
          <pre class="code-block">POST {{ selectedProject.public_base }}/responses
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json

{"model":"qmodel","input":"你好"}</pre>
          <pre class="code-block">POST {{ selectedProject.public_base }}/messages
Authorization: Bearer &lt;统一访问密码&gt;
Content-Type: application/json
anthropic-version: 2023-06-01

{"model":"qmodel","max_tokens":64,"messages":[{"role":"user","content":"你好"}]}</pre>
        </el-card>
      </template>
    </main>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus/es/components/message/index'
import 'element-plus/es/components/message/style/css'

const projects = ref([])
const manage = ref(false)
const page = ref(window.location.pathname === '/docs' ? 'docs' : 'workspace')
const selectedName = ref(new URLSearchParams(window.location.search).get('project') || '')
const actionLoading = ref('')
const refreshTimer = ref(null)
const loginForm = reactive({ password: '' })
const loginLoading = ref(false)
const loginError = ref(document.documentElement.dataset.loginError || '')

const isLocal = document.documentElement.dataset.local === 'true'
const localToken = document.documentElement.dataset.token || ''
const apiBase = (document.documentElement.dataset.apiBase || window.location.origin).replace(/\/$/, '')
const localAdminURL = `${document.documentElement.dataset.adminBase || window.location.origin}/`
const defaultProject = document.documentElement.dataset.defaultProject || 'workbuddy'

const loginPage = document.documentElement.dataset.login === 'true' || window.location.pathname === '/admin'
const loginNext = document.documentElement.dataset.next || new URLSearchParams(window.location.search).get('next') || '/'

const selectedProject = computed(() => projects.value.find((project) => project.name === selectedName.value) || projects.value[0] || null)

function tokenHeaders() {
  return localToken ? { 'X-Panel-Token': localToken } : {}
}

function syncSelectedProject() {
  if (!selectedProject.value) {
    selectedName.value = ''
    return
  }
  if (!projects.value.some((project) => project.name === selectedName.value)) {
    selectedName.value = selectedProject.value.name
  }
  const url = new URL(window.location.href)
  if (page.value === 'workspace') url.searchParams.set('project', selectedName.value)
  else url.searchParams.delete('project')
  history.replaceState(null, '', url)
}

function selectProject(name) {
  selectedName.value = name
  page.value = 'workspace'
  syncSelectedProject()
}

function openPage(nextPage) {
  page.value = nextPage
  const url = new URL(window.location.href)
  if (nextPage === 'docs') {
    url.pathname = '/docs'
    url.search = ''
  } else {
    url.pathname = '/'
    url.search = ''
    if (selectedName.value) url.searchParams.set('project', selectedName.value)
  }
  history.pushState(null, '', url)
}

function openAccounts() {
  page.value = 'docs'
  const url = new URL(window.location.href)
  url.pathname = '/docs'
  url.hash = 'accounts'
  history.pushState(null, '', url)
  requestAnimationFrame(() => document.getElementById('accounts')?.scrollIntoView({ block: 'start' }))
}

function onPopState() {
  page.value = window.location.pathname === '/docs' ? 'docs' : 'workspace'
  selectedName.value = new URLSearchParams(window.location.search).get('project') || selectedName.value
}

async function loadProjects() {
  try {
    const response = await fetch('/api/projects', {
      credentials: 'same-origin',
      headers: tokenHeaders()
    })
    if (response.status === 401) {
      window.location.href = '/admin?next=' + encodeURIComponent(window.location.pathname + window.location.search)
      return
    }
    if (!response.ok) throw new Error('加载项目状态失败')
    const data = await response.json()
    projects.value = data.projects || []
    manage.value = Boolean(data.manage)
    syncSelectedProject()
  } catch (error) {
    ElMessage.error(error.message || '加载项目状态失败')
  }
}

async function projectAction(name, action) {
  actionLoading.value = name + ':' + action
  try {
    const response = await fetch('/api/projects/' + encodeURIComponent(name) + '/' + action, {
      method: 'POST',
      credentials: 'same-origin',
      headers: tokenHeaders()
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(data.error || '操作失败')
    ElMessage.success(action === 'start' ? '启动成功' : '停止成功')
    await loadProjects()
  } catch (error) {
    ElMessage.error(error.message || '操作失败')
  } finally {
    actionLoading.value = ''
  }
}

async function startAll() {
  actionLoading.value = 'all:start'
  try {
    const response = await fetch('/api/projects/start-all', {
      method: 'POST',
      credentials: 'same-origin',
      headers: tokenHeaders()
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) throw new Error(data.error || '启动失败')
    ElMessage.success('已执行启动全部项目')
    await loadProjects()
  } catch (error) {
    ElMessage.error(error.message || '启动失败')
  } finally {
    actionLoading.value = ''
  }
}

async function submitLogin() {
  if (!loginForm.password) {
    loginError.value = '请输入访问密码。'
    return
  }
  loginLoading.value = true
  loginError.value = ''
  try {
    const response = await fetch('/admin', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ password: loginForm.password, next: loginNext }),
      redirect: 'manual'
    })
    if (response.type === 'opaqueredirect' || response.status === 0 || response.ok || response.status === 303 || response.status === 302) {
      window.location.href = loginNext
      return
    }
    if (response.status === 401) throw new Error('密码错误。')
    if (response.status === 429) throw new Error('登录尝试过多，请稍后再试。')
    throw new Error('登录失败，请稍后再试。')
  } catch (error) {
    loginError.value = error.message || '登录失败，请稍后再试。'
  } finally {
    loginLoading.value = false
  }
}

async function logout() {
  await fetch('/admin/logout', { method: 'POST', credentials: 'same-origin', redirect: 'manual' })
  window.location.href = '/admin'
}

function protocol(project, name) {
  return Boolean(project.protocols?.find((item) => item.name === name)?.native)
}

function protocolTitle(name) {
  return { chat: 'Chat Completions', responses: 'Responses', anthropic: 'Anthropic Messages' }[name] || name
}

function statusLabel(project) {
  if (project.ready) return '在线'
  if (project.running) return '运行中'
  return project.installed ? '离线' : '依赖未就绪'
}

function statusClass(project) {
  if (project.ready) return 'ok'
  if (project.running) return 'warn'
  return 'bad'
}

function tagType(project) {
  if (project.ready) return 'success'
  if (project.running) return 'warning'
  return 'danger'
}

function canStart(project) {
  return manage.value && !project.running && project.installed
}

function canStop(project) {
  return manage.value && project.running
}

onMounted(() => {
  if (loginPage) return
  loadProjects()
  refreshTimer.value = window.setInterval(loadProjects, 10000)
  window.addEventListener('popstate', onPopState)
})

onUnmounted(() => {
  if (refreshTimer.value) window.clearInterval(refreshTimer.value)
  window.removeEventListener('popstate', onPopState)
})
</script>
