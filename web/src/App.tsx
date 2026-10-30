import { useEffect, useState } from 'react'
import { clearToken, getToken, setAuthErrorHandler } from './api'
import { Login } from './views/Login'
import { OverviewView } from './views/OverviewView'
import { AnnouncementView } from './views/AnnouncementView'
import { UpdatePolicyView } from './views/UpdatePolicyView'
import { MaintenanceView } from './views/MaintenanceView'
import { PublishStatusView } from './views/PublishStatusView'
import { Logo } from './ui'
import { RecognitionOverview } from './views/StatisticsView'
import { TemplateServiceView } from './views/TemplateServiceView'

type Section = 'overview' | 'announcement' | 'update-policy' | 'maintenance' | 'publish' | 'recognition' | 'template-service'

const SECTIONS: Array<{ key: Section; label: string }> = [
  { key: 'overview', label: '概览' },
  { key: 'announcement', label: '公告管理' },
  { key: 'update-policy', label: '更新策略' },
  { key: 'maintenance', label: '维护计划' },
  { key: 'publish', label: '发布状态' },
  { key: 'recognition', label: '识别概览' },
  { key: 'template-service', label: '模板服务' },
]

const SECTION_TITLES: Record<Section, string> = {
  overview: '概览',
  announcement: '公告管理',
  'update-policy': '更新策略',
  maintenance: '维护计划',
  publish: '发布状态',
  recognition: '识别概览',
  'template-service': '模板服务',
}

export function App() {
  const [authed, setAuthed] = useState(() => getToken() !== '')
  const [section, setSection] = useState<Section>('overview')
  const [banner, setBanner] = useState<string | null>(null)

  // API 层收到 401 时回到登录页（凭据失效或被撤销）。
  useEffect(() => {
    setAuthErrorHandler(() => {
      setBanner('管理凭据已失效，请重新登录')
      setAuthed(false)
    })
  }, [])

  if (!authed) {
    return <Login onLogin={() => { setBanner(null); setAuthed(true) }} />
  }

  const logout = () => {
    clearToken()
    setAuthed(false)
  }

  return (
    <div className="shell">
      <header className="topbar">
        <div className="brand">
          <Logo />
          <strong>AzurPilot</strong>
          <span className="crumb">管理后台 / {SECTION_TITLES[section]}</span>
        </div>
        <button type="button" className="ghost" onClick={logout}>
          退出登录
        </button>
      </header>

      <div className="layout">
        <aside className="sidebar">
          <nav>
            <a className="public-site-link" href="/">公开统计网站 ↗</a>
            <p className="nav-group">运营管理</p>
            {SECTIONS.map((s) => (
              <button
                key={s.key}
                type="button"
                className={section === s.key ? 'nav-item active' : 'nav-item'}
                onClick={() => setSection(s.key)}
              >
                <span>{s.label}</span>
              </button>
            ))}
          </nav>
        </aside>

        <main className="content">
          {banner && (
            <p className="banner">
              {banner}{' '}
              <button type="button" className="ghost" onClick={() => setBanner(null)}>
                关闭
              </button>
            </p>
          )}
          {section === 'overview' && <OverviewView onNavigate={(s) => setSection(s as Section)} />}
          {section === 'announcement' && <AnnouncementView />}
          {section === 'update-policy' && <UpdatePolicyView />}
          {section === 'maintenance' && <MaintenanceView />}
          {section === 'publish' && <PublishStatusView />}
          {section === 'recognition' && <RecognitionOverview onClassify={() => setSection('template-service')} />}
          {section === 'template-service' && <TemplateServiceView />}
        </main>
      </div>
    </div>
  )
}
