import { useState } from 'react'
import { App, Button, Input, Typography } from 'antd'
import { post } from '../api'

interface ImportResult {
  imported: number
}

// 委托目录用于计算时均产出；没有目录时 amount_per_hour 返回 null。
export default function CatalogView() {
  const { message } = App.useApp()
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit() {
    let payload: unknown
    try {
      payload = JSON.parse(text)
    } catch (err) {
      message.error(`JSON 解析失败：${(err as Error).message}`)
      return
    }
    setBusy(true)
    try {
      const data = await post<ImportResult>('/api/v1/templates/import', payload)
      message.success(`已导入 ${data.imported} 条委托`)
    } catch (err) {
      message.error((err as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Typography.Paragraph type="secondary">
        粘贴 commission_names.json 内容，格式：
        <code>{'{"commissions":[{"comm","name","duration_seconds","expiration_seconds","code"}]}'}</code>。
        导入后用于计算时均产出。
      </Typography.Paragraph>
      <Input.TextArea
        rows={14}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder='{"commissions":[...]}'
        style={{ fontFamily: 'ui-monospace, Menlo, Consolas, monospace' }}
      />
      <Button type="primary" style={{ marginTop: 12 }} loading={busy} onClick={() => void submit()}>
        导入目录
      </Button>
    </>
  )
}
