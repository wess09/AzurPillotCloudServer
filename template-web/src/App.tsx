import { useCallback, useEffect, useState } from 'react'
import { App as AntApp, Button, Form, Input, Modal, Space, Tabs, Tag } from 'antd'
import { CloudUploadOutlined, LogoutOutlined, ReloadOutlined } from '@ant-design/icons'
import { get, getToken, post, setToken } from './api'
import type { StatusView } from './types'
import LoginCard from './components/LoginCard'
import TemplatesView from './views/TemplatesView'
import UnknownView from './views/UnknownView'
import ReleasesView from './views/ReleasesView'
import CatalogView from './views/CatalogView'

function Logo() {
  return (
    <svg width="30" height="20" viewBox="0 0 48 32" fill="none" aria-hidden="true">
      <g fill="#FBAD41">
        <circle cx="21" cy="10" r="6.5" />
        <rect x="12" y="8" width="20" height="9" rx="4.5" />
      </g>
      <g fill="#F6821F">
        <circle cx="13" cy="20" r="7" />
        <circle cx="25" cy="16" r="9.5" />
        <rect x="7" y="19" width="30" height="8" rx="4" />
        <rect x="7" y="24" width="37" height="4" rx="2" />
      </g>
    </svg>
  )
}

export default function Root() {
  const { message } = AntApp.useApp()
  const [token, setTok] = useState(getToken())
  const [status, setStatus] = useState<StatusView | null>(null)
  const [tab, setTab] = useState('templates')
  const [dirty, setDirty] = useState(false)
  const [refreshKey, setRefreshKey] = useState(0)
  const [publishOpen, setPublishOpen] = useState(false)
  const [publishing, setPublishing] = useState(false)
  const [publishForm] = Form.useForm<{ note?: string }>()

  const loadStatus = useCallback(async () => {
    if (!getToken()) return
    try {
      setStatus(await get<StatusView>('/api/v1/status'))
    } catch (err) {
      setStatus(null)
      message.error((err as Error).message)
    }
  }, [message])

  useEffect(() => {
    void loadStatus()
  }, [loadStatus, refreshKey, token])

  useEffect(() => {
    const expired = () => {
      setTok('')
      setStatus(null)
    }
    window.addEventListener('template-auth-expired', expired)
    return () => window.removeEventListener('template-auth-expired', expired)
  }, [])

  function logout() {
    setToken('')
    setTok('')
    setStatus(null)
    setDirty(false)
  }

  function changed() {
    setDirty(true)
    setRefreshKey((v) => v + 1)
  }

  async function publish() {
    const values = await publishForm.validateFields()
    setPublishing(true)
    try {
      const data = await post<{ revision: number }>('/api/v1/templates/publish', {
        note: (values.note ?? '').trim(),
      })
      message.success(`已发布 revision ${data.revision}`)
      setPublishOpen(false)
      setDirty(false)
      setRefreshKey((v) => v + 1)
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setPublishing(false)
    }
  }

  if (!token) {
    return <LoginCard onLogin={() => setTok(getToken())} />
  }

  return (
    <>
      <header className="app-header">
        <div className="brand">
          <Logo />
          模板服务器
        </div>
        <Tag color="blue">revision {status?.revision ?? '—'}</Tag>
        <Tag color={status?.cf_deploy ? 'green' : 'default'}>
          {status?.cf_deploy ? 'Cloudflare 分发' : '仅本地发布'}
        </Tag>
        {dirty ? <Tag color="gold">有修改未发布</Tag> : null}
        <div className="spacer" />
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => setRefreshKey((v) => v + 1)}>
            刷新
          </Button>
          <Button type="primary" icon={<CloudUploadOutlined />} onClick={() => setPublishOpen(true)}>
            发布分发
          </Button>
          <Button icon={<LogoutOutlined />} onClick={logout}>
            退出
          </Button>
        </Space>
      </header>

      <main className="app-main">
        <div className="status-line">
          <span>
            启用模板 <b>{status?.enabled ?? '—'}</b>
          </span>
          <span>
            待处理聚类 <b>{status?.pending_clusters ?? '—'}</b>
          </span>
          <span>
            Worker <b>{status?.worker_name ?? '—'}</b>
          </span>
          <span>
            分发地址 <b>{status?.public_base || '未配置'}</b>
          </span>
        </div>

        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            {
              key: 'templates',
              label: '模板',
              children: <TemplatesView refreshKey={refreshKey} onChanged={changed} />,
            },
            {
              key: 'unknown',
              label: '未知模板',
              children: <UnknownView refreshKey={refreshKey} onChanged={changed} />,
            },
            {
              key: 'releases',
              label: '发布记录',
              children: <ReleasesView refreshKey={refreshKey} />,
            },
            {
              key: 'catalog',
              label: '委托目录',
              children: <CatalogView />,
            },
          ]}
        />
      </main>

      <Modal
        title="发布分发"
        open={publishOpen}
        onCancel={() => setPublishOpen(false)}
        onOk={() => void publish()}
        confirmLoading={publishing}
        okText="发布"
        destroyOnClose
      >
        <p style={{ color: '#737373' }}>
          生成新 revision、签名 manifest，并部署到 Cloudflare Worker 与本地镜像。映射与模板变更在此之后对客户端生效。
        </p>
        <Form form={publishForm} layout="vertical">
          <Form.Item name="note" label="发布说明">
            <Input maxLength={256} placeholder="可选" />
          </Form.Item>
        </Form>
      </Modal>
    </>
  )
}
