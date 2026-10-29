import { useCallback, useEffect, useState } from 'react'
import { App, Button, Space, Table, Tag, Typography } from 'antd'
import type { TableProps } from 'antd'
import { get } from '../api'
import type { ReleaseView } from '../types'

interface Props {
  refreshKey: number
}

function formatTime(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString('zh-CN', { hour12: false })
}

export default function ReleasesView({ refreshKey }: Props) {
  const { message } = App.useApp()
  const [rows, setRows] = useState<ReleaseView[]>([])
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await get<{ releases: ReleaseView[] }>('/api/v1/templates/releases')
      setRows(data.releases ?? [])
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const columns: TableProps<ReleaseView>['columns'] = [
    { title: 'revision', dataIndex: 'Revision', width: 100, render: (v: number) => <b>{v}</b> },
    {
      title: '时间',
      dataIndex: 'CreatedAt',
      width: 180,
      render: (v: string) => <span style={{ color: '#737373' }}>{formatTime(v)}</span>,
    },
    { title: '说明', dataIndex: 'Note' },
    {
      title: '部署',
      dataIndex: 'Deployed',
      width: 100,
      render: (v: boolean) => (v ? <Tag color="green">已部署</Tag> : <Tag color="gold">未部署</Tag>),
    },
    {
      title: 'manifest sha256',
      dataIndex: 'ManifestSHA256',
      width: 220,
      render: (v: string) => <Typography.Text code className="mono">{v ? `${v.slice(0, 16)}…` : '—'}</Typography.Text>,
    },
    {
      title: '地址',
      dataIndex: 'WorkerURL',
      render: (v: string) =>
        v ? (
          <a href={v} target="_blank" rel="noreferrer" className="mono">
            {v}
          </a>
        ) : (
          '—'
        ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }}>
        <Button onClick={() => void load()}>刷新</Button>
      </Space>
      <Table
        rowKey="Revision"
        size="middle"
        loading={loading}
        columns={columns}
        dataSource={rows}
        pagination={{ pageSize: 20 }}
      />
    </>
  )
}
