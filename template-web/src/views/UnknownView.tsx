import { useCallback, useEffect, useState } from 'react'
import {
  App,
  AutoComplete,
  Button,
  Form,
  Image,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { TableProps } from 'antd'
import { get, post } from '../api'
import type { ClusterView } from '../types'

interface Props {
  refreshKey: number
  onChanged: () => void
}

const STATUS_META: Record<string, { color: string; label: string }> = {
  pending: { color: 'gold', label: '待处理' },
  mapped: { color: 'green', label: '已映射' },
  rejected: { color: 'red', label: '已拒绝' },
  merged: { color: 'default', label: '已合并' },
}

const SCENE_LABEL: Record<string, string> = {
  commission: '委托',
  research: '科研',
  all: '通用',
}

function formatTime(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString('zh-CN', { hour12: false })
}

export default function UnknownView({ refreshKey, onChanged }: Props) {
  const { message } = App.useApp()
  const [status, setStatus] = useState('pending')
  const [rows, setRows] = useState<ClusterView[]>([])
  const [loading, setLoading] = useState(false)
  const [mapTarget, setMapTarget] = useState<ClusterView | null>(null)
  const [mergeTarget, setMergeTarget] = useState<ClusterView | null>(null)
  const [allKeys, setAllKeys] = useState<string[]>([])
  const [mapForm] = Form.useForm<MapValues>()
  const [mergeForm] = Form.useForm<{ to_key: string }>()
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const qs = status ? `?status=${encodeURIComponent(status)}` : ''
      const data = await get<{ clusters: ClusterView[] }>(`/api/v1/templates/unknown${qs}`)
      setRows(data.clusters ?? [])
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setLoading(false)
    }
  }, [message, status])

  useEffect(() => {
    void load()
  }, [load, refreshKey])

  async function openMerge(cluster: ClusterView) {
    setMergeTarget(cluster)
    try {
      const data = await get<{ clusters: ClusterView[] }>('/api/v1/templates/unknown?limit=500')
      setAllKeys((data.clusters ?? []).map((c) => c.cluster_key).filter((key) => key !== cluster.cluster_key))
    } catch {
      setAllKeys([])
    }
  }

  function openMap(cluster: ClusterView) {
    setMapTarget(cluster)
    mapForm.setFieldsValue({
      item: cluster.item ?? '',
      tag: cluster.tag ?? '',
      name: cluster.item ?? '',
      scene: cluster.scene || 'all',
      kind: 'temporary',
      register: true,
    })
  }

  async function saveMap() {
    if (!mapTarget) return
    const values = await mapForm.validateFields()
    setBusy(true)
    try {
      await post(`/api/v1/templates/unknown/${encodeURIComponent(mapTarget.cluster_key)}/map`, {
        item: values.item.trim(),
        tag: (values.tag ?? '').trim(),
        name: (values.name ?? '').trim(),
        scene: values.scene,
        kind: values.kind,
        register: values.register,
      })
      message.success('映射已保存，发布后生效')
      setMapTarget(null)
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function saveMerge() {
    if (!mergeTarget) return
    const values = await mergeForm.validateFields()
    setBusy(true)
    try {
      await post('/api/v1/templates/unknown/merge', {
        from_key: mergeTarget.cluster_key,
        to_key: values.to_key.trim(),
      })
      message.success('已合并')
      setMergeTarget(null)
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  async function reject(cluster: ClusterView) {
    try {
      await post(`/api/v1/templates/unknown/${encodeURIComponent(cluster.cluster_key)}/reject`)
      message.success('已拒绝，发布的 mappings 不再包含它')
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  const columns: TableProps<ClusterView>['columns'] = [
    {
      title: '',
      dataIndex: 'image_base64',
      width: 60,
      render: (value: string) => (
        <Image className="pixel" width={40} height={40} src={`data:image/png;base64,${value}`} preview={false} alt="" />
      ),
    },
    {
      title: '聚类 key',
      dataIndex: 'cluster_key',
      render: (value: string) => (
        <Typography.Text code copyable className="mono">
          {value}
        </Typography.Text>
      ),
    },
    {
      title: '场景',
      dataIndex: 'scene',
      width: 90,
      render: (value: string) => <Tag>{SCENE_LABEL[value] ?? value}</Tag>,
    },
    { title: '次数', dataIndex: 'occurrences', width: 80 },
    {
      title: '状态',
      dataIndex: 'status',
      width: 100,
      render: (value: string) => {
        const meta = STATUS_META[value] ?? { color: 'default', label: value }
        return <Tag color={meta.color}>{meta.label}</Tag>
      },
    },
    {
      title: '物品',
      dataIndex: 'item',
      width: 160,
      render: (value: string, row) => (value ? <span>{value}{row.tag ? ` / ${row.tag}` : ''}</span> : '—'),
    },
    {
      title: '最近出现',
      dataIndex: 'last_seen_at',
      width: 170,
      render: (value: string) => <span style={{ color: '#737373' }}>{formatTime(value)}</span>,
    },
    {
      title: '操作',
      width: 200,
      render: (_, row) => (
        <Space size={4} wrap>
          {row.status === 'pending' || row.status === 'mapped' ? (
            <Button size="small" type="primary" onClick={() => openMap(row)}>
              映射
            </Button>
          ) : null}
          {row.status !== 'merged' && row.status !== 'rejected' ? (
            <Button size="small" onClick={() => void openMerge(row)}>
              合并
            </Button>
          ) : null}
          {row.status !== 'rejected' ? (
            <Popconfirm title="拒绝该聚类？" onConfirm={() => void reject(row)}>
              <Button size="small" danger>
                拒绝
              </Button>
            </Popconfirm>
          ) : null}
        </Space>
      ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Select
          style={{ width: 140 }}
          value={status}
          onChange={setStatus}
          options={[
            { value: 'pending', label: '待处理' },
            { value: 'mapped', label: '已映射' },
            { value: 'rejected', label: '已拒绝' },
            { value: 'merged', label: '已合并' },
            { value: '', label: '全部' },
          ]}
        />
        <Button onClick={() => void load()}>刷新</Button>
      </Space>
      <Table
        rowKey="cluster_key"
        size="middle"
        loading={loading}
        columns={columns}
        dataSource={rows}
        pagination={{ pageSize: 20, showTotal: (n) => `共 ${n} 个聚类` }}
      />

      <Modal
        title="映射未知模板"
        open={mapTarget !== null}
        onCancel={() => setMapTarget(null)}
        onOk={() => void saveMap()}
        confirmLoading={busy}
        okText="保存映射"
        destroyOnClose
      >
        <p style={{ color: '#737373' }}>
          聚类：<code>{mapTarget?.cluster_key}</code>
        </p>
        <Form form={mapForm} layout="vertical">
          <Form.Item name="item" label="物品（item）" rules={[{ required: true, message: '请输入物品' }]}>
            <Input maxLength={128} placeholder="例如 Coins" />
          </Form.Item>
          <Space.Compact style={{ width: '100%' }}>
            <Form.Item name="tag" label="标签（tag）" style={{ flex: 1 }}>
              <Input maxLength={32} />
            </Form.Item>
            <Form.Item name="name" label="模板名称" style={{ flex: 1, marginLeft: 8 }}>
              <Input maxLength={128} placeholder="默认与 item 相同" />
            </Form.Item>
          </Space.Compact>
          <Space.Compact style={{ width: '100%', marginTop: 8 }}>
            <Form.Item name="scene" label="场景" style={{ flex: 1 }}>
              <Select
                options={[
                  { value: 'all', label: 'all' },
                  { value: 'commission', label: 'commission' },
                  { value: 'research', label: 'research' },
                ]}
              />
            </Form.Item>
            <Form.Item name="kind" label="类型" style={{ flex: 1, marginLeft: 8 }}>
              <Select
                options={[
                  { value: 'temporary', label: 'temporary' },
                  { value: 'formal', label: 'formal' },
                ]}
              />
            </Form.Item>
          </Space.Compact>
          <Form.Item name="register" label="同时注册为模板" valuePropName="checked" style={{ marginTop: 8 }}>
            <Switch />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="合并聚类"
        open={mergeTarget !== null}
        onCancel={() => setMergeTarget(null)}
        onOk={() => void saveMerge()}
        confirmLoading={busy}
        okText="合并"
        destroyOnClose
      >
        <p style={{ color: '#737373' }}>
          来源：<code>{mergeTarget?.cluster_key}</code>
        </p>
        <Form form={mergeForm} layout="vertical">
          <Form.Item
            name="to_key"
            label="目标 cluster_key"
            rules={[{ required: true, message: '请输入目标 key' }]}
            extra="合并后来源计数转移到目标，来源映射迁移到目标。"
          >
            <AutoComplete
              options={allKeys.map((key) => ({ value: key }))}
              placeholder="U-…"
              filterOption={(input, option) => String(option?.value ?? '').includes(input)}
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  )
}

interface MapValues {
  item: string
  tag?: string
  name?: string
  scene: string
  kind: string
  register: boolean
}
