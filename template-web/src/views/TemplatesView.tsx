import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  App,
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
  Upload,
} from 'antd'
import type { TableProps, UploadProps } from 'antd'
import { PlusOutlined, UploadOutlined } from '@ant-design/icons'
import { ApiError, del, get, patch, post } from '../api'
import type { TemplateView } from '../types'

interface Props {
  refreshKey: number
  onChanged: () => void
}

interface EditorValues {
  key?: string
  name: string
  item: string
  tag?: string
  scene: string
  kind: string
  allow_duplicate?: boolean
}

const SCENE_TAG: Record<string, { color: string; label: string }> = {
  commission: { color: 'blue', label: '委托' },
  research: { color: 'purple', label: '科研' },
  all: { color: 'default', label: '通用' },
}

function formatTime(value: string): string {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString('zh-CN', { hour12: false })
}

export default function TemplatesView({ refreshKey, onChanged }: Props) {
  const { message } = App.useApp()
  const [rows, setRows] = useState<TemplateView[]>([])
  const [loading, setLoading] = useState(false)
  const [search, setSearch] = useState('')
  const [scene, setScene] = useState<string>()
  const [enabled, setEnabled] = useState<string>()
  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<TemplateView | null>(null)
  const [renameTarget, setRenameTarget] = useState<TemplateView | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameBusy, setRenameBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await get<{ templates: TemplateView[] }>('/api/v1/templates?limit=2000')
      setRows(data.templates ?? [])
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setLoading(false)
    }
  }, [message])

  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return rows.filter((row) => {
      if (scene && row.scene !== scene) return false
      if (enabled && String(row.enabled) !== enabled) return false
      if (!q) return true
      return [row.key, row.name, row.item, row.tag].some((v) => String(v ?? '').toLowerCase().includes(q))
    })
  }, [rows, search, scene, enabled])

  async function toggle(row: TemplateView, next: boolean) {
    try {
      await patch(`/api/v1/templates/${encodeURIComponent(row.key)}`, { enabled: next })
      message.success(next ? '已启用' : '已禁用')
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  async function remove(row: TemplateView) {
    try {
      await del(`/api/v1/templates/${encodeURIComponent(row.key)}`)
      message.success('已禁用，发布后从客户端移除')
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    }
  }

  async function rename() {
    if (!renameTarget) return
    setRenameBusy(true)
    try {
      await post(`/api/v1/templates/${encodeURIComponent(renameTarget.key)}/rename`, {
        new_key: renameValue.trim(),
      })
      message.success('模板 ID 已更新，发布后生效')
      setRenameTarget(null)
      onChanged()
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setRenameBusy(false)
    }
  }

  const columns: TableProps<TemplateView>['columns'] = [
    {
      title: '',
      dataIndex: 'image_base64',
      width: 60,
      render: (value: string) => (
        <Image
          className="pixel"
          width={40}
          height={40}
          src={`data:image/png;base64,${value}`}
          preview={false}
          alt=""
        />
      ),
    },
    {
      title: 'ID / 哈希',
      dataIndex: 'key',
      render: (value: string, row) => (
        <div>
          <Typography.Text code copyable className="mono">
            {value}
          </Typography.Text>
          <div className="mono" style={{ color: '#a3a3a3' }}>
            {row.sha256.slice(0, 12)}…
          </div>
        </div>
      ),
    },
    { title: '名称', dataIndex: 'name', width: 160 },
    {
      title: '物品',
      dataIndex: 'item',
      width: 200,
      render: (value: string, row) => (
        <Space size={4} wrap>
          <span>{value}</span>
          {row.tag ? <Tag>{row.tag}</Tag> : null}
        </Space>
      ),
    },
    {
      title: '场景 / 类型',
      dataIndex: 'scene',
      width: 150,
      render: (value: string, row) => {
        const meta = SCENE_TAG[value] ?? { color: 'default', label: value }
        return (
          <Space size={4} wrap>
            <Tag color={meta.color}>{meta.label}</Tag>
            {row.kind === 'temporary' ? <Tag color="orange">临时</Tag> : <Tag>正式</Tag>}
          </Space>
        )
      },
    },
    {
      title: '启用',
      dataIndex: 'enabled',
      width: 90,
      render: (value: boolean, row) => (
        <Switch size="small" checked={value} onChange={(next) => void toggle(row, next)} />
      ),
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      width: 170,
      render: (value: string) => <span style={{ color: '#737373' }}>{formatTime(value)}</span>,
    },
    {
      title: '操作',
      width: 190,
      render: (_, row) => (
        <Space size={4} wrap>
          <Button size="small" onClick={() => { setEditing(row); setEditorOpen(true) }}>
            编辑
          </Button>
          <Button
            size="small"
            onClick={() => {
              setRenameTarget(row)
              setRenameValue(row.key)
            }}
          >
            改 ID
          </Button>
          <Popconfirm title="禁用该模板？" description="发布后客户端将不再收到它。" onConfirm={() => void remove(row)}>
            <Button size="small" danger>
              禁用
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <>
      <Space style={{ marginBottom: 12 }} wrap>
        <Input.Search
          allowClear
          placeholder="搜索 key / 名称 / 物品"
          style={{ width: 260 }}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Select
          allowClear
          placeholder="全部场景"
          style={{ width: 130 }}
          value={scene}
          onChange={setScene}
          options={[
            { value: 'all', label: 'all' },
            { value: 'commission', label: 'commission' },
            { value: 'research', label: 'research' },
          ]}
        />
        <Select
          allowClear
          placeholder="全部状态"
          style={{ width: 120 }}
          value={enabled}
          onChange={setEnabled}
          options={[
            { value: 'true', label: '已启用' },
            { value: 'false', label: '已禁用' },
          ]}
        />
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(null)
            setEditorOpen(true)
          }}
        >
          上传模板
        </Button>
      </Space>
      <Table
        rowKey="key"
        size="middle"
        loading={loading}
        columns={columns}
        dataSource={filtered}
        pagination={{ pageSize: 20, showSizeChanger: true, showTotal: (n) => `共 ${n} 个模板` }}
      />

      <TemplateEditor
        open={editorOpen}
        template={editing}
        onClose={() => setEditorOpen(false)}
        onSaved={() => {
          setEditorOpen(false)
          onChanged()
        }}
      />

      <Modal
        title="修改模板 ID"
        open={renameTarget !== null}
        onCancel={() => setRenameTarget(null)}
        onOk={() => void rename()}
        confirmLoading={renameBusy}
        okText="保存"
        destroyOnClose
      >
        <p style={{ color: '#737373' }}>
          当前 ID：<code>{renameTarget?.key}</code>。新 ID 会在下次发布后进入 manifest。
        </p>
        <Input value={renameValue} onChange={(e) => setRenameValue(e.target.value)} placeholder="T-Coins-01" />
      </Modal>
    </>
  )
}

interface EditorProps {
  open: boolean
  template: TemplateView | null
  onClose: () => void
  onSaved: () => void
}

function TemplateEditor({ open, template, onClose, onSaved }: EditorProps) {
  const { message } = App.useApp()
  const [form] = Form.useForm<EditorValues>()
  const [icon, setIcon] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!open) return
    setIcon('')
    form.setFieldsValue({
      key: template?.key ?? '',
      name: template?.name ?? '',
      item: template?.item ?? '',
      tag: template?.tag ?? '',
      scene: template?.scene ?? 'all',
      kind: template?.kind ?? 'formal',
      allow_duplicate: false,
    })
  }, [open, template, form])

  const preview = icon || template?.image_base64 || ''

  const beforeUpload: UploadProps['beforeUpload'] = async (file) => {
    try {
      const bitmap = await createImageBitmap(file)
      const canvas = document.createElement('canvas')
      canvas.width = 49
      canvas.height = 49
      const ctx = canvas.getContext('2d')
      if (!ctx) throw new Error('浏览器不支持 canvas')
      ctx.imageSmoothingEnabled = true
      ctx.imageSmoothingQuality = 'high'
      ctx.drawImage(bitmap, 0, 0, 49, 49)
      bitmap.close()
      setIcon(canvas.toDataURL('image/png').split(',')[1])
    } catch (err) {
      message.error(`图片处理失败：${(err as Error).message}`)
    }
    return Upload.LIST_IGNORE
  }

  async function submit() {
    const values = await form.validateFields()
    setSaving(true)
    try {
      if (template) {
        if (icon) {
          await post('/api/v1/templates', {
            key: template.key,
            name: values.name,
            item: values.item,
            tag: values.tag ?? '',
            scene: values.scene,
            kind: values.kind,
            image_base64: icon,
            allow_duplicate: true,
          })
        } else {
          await patch(`/api/v1/templates/${encodeURIComponent(template.key)}`, {
            name: values.name,
            item: values.item,
            tag: values.tag ?? '',
            scene: values.scene,
            kind: values.kind,
          })
        }
        message.success('模板已更新，发布后生效')
      } else {
        if (!icon) {
          message.warning('请选择图标')
          return
        }
        await post('/api/v1/templates', {
          key: values.key?.trim() || undefined,
          name: values.name,
          item: values.item,
          tag: values.tag ?? '',
          scene: values.scene,
          kind: values.kind,
          image_base64: icon,
          allow_duplicate: values.allow_duplicate ? true : undefined,
        })
        message.success('模板已创建，发布后生效')
      }
      onSaved()
    } catch (err) {
      const apiErr = err as ApiError
      const existing = apiErr.details?.existing_key
      message.error(existing ? `${apiErr.message}（已存在：${String(existing)}）` : apiErr.message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      title={template ? '编辑模板' : '上传模板'}
      open={open}
      onCancel={onClose}
      onOk={() => void submit()}
      confirmLoading={saving}
      okText="保存"
      width={560}
      destroyOnClose
    >
      <Form form={form} layout="vertical" style={{ marginTop: 12 }}>
        <Form.Item label="图标（自动缩放为 49×49 PNG）" required={!template}>
          <Space align="center">
            {preview ? (
              <Image className="pixel" width={56} height={56} src={`data:image/png;base64,${preview}`} alt="" />
            ) : (
              <div
                className="pixel"
                style={{ width: 56, height: 56, display: 'flex', alignItems: 'center', justifyContent: 'center', color: '#a3a3a3' }}
              >
                无
              </div>
            )}
            <Upload accept="image/png,image/*" showUploadList={false} beforeUpload={beforeUpload}>
              <Button icon={<UploadOutlined />}>选择图片</Button>
            </Upload>
          </Space>
        </Form.Item>
        <Form.Item name="key" label="模板 ID（key）">
          <Input
            disabled={Boolean(template)}
            placeholder="留空按图标哈希生成 T-xxxxxxxx"
            maxLength={128}
          />
        </Form.Item>
        <Form.Item name="name" label="名称" rules={[{ required: true, message: '请输入名称' }]}>
          <Input maxLength={128} />
        </Form.Item>
        <Form.Item name="item" label="物品（item）" rules={[{ required: true, message: '请输入物品' }]}>
          <Input maxLength={128} placeholder="例如 Coins" />
        </Form.Item>
        <Space.Compact style={{ width: '100%' }}>
          <Form.Item name="tag" label="标签（tag）" style={{ flex: 1 }}>
            <Input maxLength={32} />
          </Form.Item>
          <Form.Item name="scene" label="场景" style={{ flex: 1, marginLeft: 8 }}>
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
                { value: 'formal', label: 'formal' },
                { value: 'temporary', label: 'temporary' },
              ]}
            />
          </Form.Item>
        </Space.Compact>
        {!template ? (
          <Form.Item name="allow_duplicate" label="允许重复图标" valuePropName="checked" style={{ marginTop: 8 }}>
            <Switch />
          </Form.Item>
        ) : null}
      </Form>
    </Modal>
  )
}
