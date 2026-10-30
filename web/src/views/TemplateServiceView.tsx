import { useEffect, useState } from 'react'
import { request } from '../api'
import { Card, useAction, useLoad } from '../ui'
import '../template-service.css'

// Requests go straight from the browser to the template host, using the shared admin login.
const BASE = 'https://templates-api.azurpilot.com/internal/v1/templates'
type Mapping = { item_id: string; name: string; tag: string }
type Template = { id: string; canonical_id: string; name: string; tag: string; kind: 'formal' | 'temporary'; hits: number; image_base64: string; draft: Mapping | null }
type Release = { revision: number; status: 'building' | 'deployed' | 'failed'; note: string; error: string; created_at: string }
type Status = { revision: number; drafts: number; pending_events: number; pending_publications: number; deploy_mode: string; public_base: string; main_event_configured: boolean; release: Release | null; last_delivery: { last_error: string; attempts: number } | null }

export function TemplateServiceView() {
  const [filter, setFilter] = useState('temporary')
  const [offset, setOffset] = useState(0)
  const [pulse, setPulse] = useState(0)
  const [selected, setSelected] = useState<Template | null>(null)
  const [mapping, setMapping] = useState<Mapping>({ item_id: '', name: '', tag: '' })
  const [formal, setFormal] = useState<Mapping>({ item_id: '', name: '', tag: '' })
  const [file, setFile] = useState<File | null>(null)
  const status = useLoad(() => request<Status>('GET', `${BASE}/status`), [pulse])
  const rows = useLoad(() => request<{ items: Template[] }>('GET', `${BASE}?limit=50&offset=${offset}${filter ? `&kind=${filter}` : ''}`), [filter, offset, pulse])
  const releases = useLoad(() => request<{ items: Release[] }>('GET', `${BASE}/releases`), [pulse])
  const action = useAction()
  useEffect(() => {
    const timer = window.setInterval(() => setPulse(value => value + 1), 5000)
    return () => window.clearInterval(timer)
  }, [])
  const refresh = () => setPulse(value => value + 1)
  const choose = (row: Template) => {
    setSelected(row)
    setMapping(row.draft ?? { item_id: row.kind === 'formal' ? row.canonical_id : '', name: row.name, tag: row.tag })
  }
  return <>
    <div className="page-head"><div><h1>模板服务</h1><p className="muted">统一未知物品编号，发布映射后修正历史统计。模板服务独立运行，资源由 Cloudflare Worker 分发。</p></div><button type="button" className="ghost" onClick={refresh}>刷新</button></div>
    {status.error && <p className="banner">{status.error}</p>}
    {action.message && <p role="status" className="banner">{action.message.text}</p>}
    {status.data && <Card title="分发与同步状态" extra={<span className="pill">版本 {status.data.revision}</span>}>
      <div className="template-service-metrics"><div><strong>{status.data.drafts}</strong><span>待发布映射</span></div><div><strong>{status.data.pending_publications}</strong><span>排队发布</span></div><div><strong>{status.data.pending_events}</strong><span>待主服务器确认</span></div></div>
      <p className="muted">分发地址：<a href={status.data.public_base} target="_blank" rel="noreferrer">{status.data.public_base}</a></p>
      {status.data.deploy_mode !== 'cloudflare' && <p className="banner">当前仅生成本地资源，尚未部署至 Cloudflare Worker。</p>}
      {!status.data.main_event_configured && <p className="banner">模板服务尚未配置主服务器推送地址或通讯凭据，事件会保留等待同步。</p>}
      {status.data.release?.error && <p className="banner">{status.data.release.error}</p>}
      {status.data.last_delivery?.last_error && <p className="banner">主服务器同步失败，正在重试：{status.data.last_delivery.last_error}</p>}
      <button type="button" disabled={action.running} onClick={() => void action.run(async () => {
        const result = await request<{ request_id: number }>('POST', `${BASE}/publish`, { note: '统一管理后台发布模板映射' })
        refresh()
        return `发布任务 #${result.request_id} 已排队。当前草稿已冻结，发布后同步客户端与主服务器。`
      })}>发布当前映射{status.data.drafts ? `（${status.data.drafts} 条）` : ''}</button>
    </Card>}
    <Card title="模板与临时编号" extra={<select aria-label="模板类型" value={filter} onChange={event => { setFilter(event.target.value); setOffset(0); setSelected(null) }}><option value="temporary">未知模板</option><option value="formal">正式模板</option><option value="">全部模板</option></select>}>
      {rows.error && <p className="banner">{rows.error}</p>}
      {rows.loading && !rows.data && <p className="muted">正在读取模板…</p>}
      {rows.data && rows.data.items.length === 0 && <div className="empty"><p>暂无{filter === 'formal' ? '正式' : '未知'}模板。</p><p className="muted">客户端上传未知图标后会自动分配 U-编号；也可以在下方导入正式模板。</p></div>}
      <div className="template-service-grid">{rows.data?.items.map(row => <button type="button" key={row.id} aria-pressed={selected?.id === row.id} className={`template-service-tile ${selected?.id === row.id ? 'selected' : ''}`} onClick={() => choose(row)}>
        <img src={`data:image/png;base64,${row.image_base64}`} alt={row.name || row.id} width={49} height={49} /><strong>{row.name || row.canonical_id}</strong><code>{row.id}</code><span className="muted">{row.hits} 次匹配{row.draft ? ' · 映射待发布' : row.kind === 'formal' ? ' · 已正式命名' : ' · 临时编号'}</span>
      </button>)}</div>
      <div className="form-actions"><button type="button" className="ghost" disabled={!offset} onClick={() => setOffset(value => Math.max(0, value - 50))}>上一页</button><span className="muted">第 {offset / 50 + 1} 页</span><button type="button" className="ghost" disabled={(rows.data?.items.length ?? 0) < 50} onClick={() => setOffset(value => value + 50)}>下一页</button></div>
    </Card>
    {selected && selected.id.startsWith('U-') && <Card title={`映射 ${selected.id}`} extra={<button type="button" className="ghost" onClick={() => setSelected(null)}>关闭</button>}>
      <p className="muted">保存后先保留为草稿。发布时会替换模板识别 ID，并下发历史统计编号映射；原编号会作为别名保留。</p>
      <form className="stack" onSubmit={event => { event.preventDefault(); void action.run(async () => {
        await request('PUT', `${BASE}/mappings/${encodeURIComponent(selected.id)}`, mapping)
        refresh()
        return `${selected.id} → ${mapping.item_id} 已保存为草稿，点击“发布当前映射”后生效。`
      }) }}>
        <label className="field">正式物品 ID<input required maxLength={128} value={mapping.item_id} placeholder="例如 BlueprintT3" onChange={event => setMapping({ ...mapping, item_id: event.target.value })} /></label>
        <label className="field">物品名称<input required maxLength={128} value={mapping.name} placeholder="例如 某某图纸" onChange={event => setMapping({ ...mapping, name: event.target.value })} /></label>
        <label className="field">标签（可选）<input maxLength={32} value={mapping.tag} onChange={event => setMapping({ ...mapping, tag: event.target.value })} /></label>
        <div><button disabled={action.running}>保存映射草稿</button></div>
      </form>
    </Card>}
    <Card title="导入正式模板"><p className="muted">支持 49×49 PNG 图标或 96×96 物品格。导入后自动排队分发；同一物品 ID 可有多个图标变体。</p>
      <form className="stack" onSubmit={event => { event.preventDefault(); void action.run(async () => {
        if (!file) throw new Error('请选择 PNG 模板')
        if (file.size > 256 * 1024) throw new Error('模板文件不能超过 256KB')
        const image_base64 = await new Promise<string>((resolve, reject) => {
          const reader = new FileReader()
          reader.onerror = () => reject(new Error('无法读取模板文件'))
          reader.onload = () => resolve(String(reader.result).split(',')[1])
          reader.readAsDataURL(file)
        })
        await request('POST', `${BASE}`, { ...formal, image_base64 })
        refresh()
        return `正式模板 ${formal.item_id} 已导入，等待静态发布。`
      }) }}>
        <label className="field">正式物品 ID<input required maxLength={128} value={formal.item_id} onChange={event => setFormal({ ...formal, item_id: event.target.value })} /></label>
        <label className="field">物品名称<input required maxLength={128} value={formal.name} onChange={event => setFormal({ ...formal, name: event.target.value })} /></label>
        <label className="field">PNG 模板<input type="file" required accept="image/png" onChange={event => setFile(event.target.files?.[0] ?? null)} /></label>
        <div><button disabled={action.running}>导入模板</button></div>
      </form>
    </Card>
    <Card title="发布记录"><div className="table-wrap"><table><thead><tr><th>版本</th><th>状态</th><th>说明</th><th>时间</th></tr></thead><tbody>{releases.data?.items.map(release => <tr key={release.revision}><td>{release.revision}</td><td><span className={`pill ${release.status === 'deployed' ? 'pill-ok' : release.status === 'failed' ? 'pill-err' : 'pill-warn'}`}>{release.status === 'deployed' ? '已发布' : release.status === 'failed' ? '失败，等待重试' : '发布中'}</span></td><td>{release.note}</td><td>{new Date(release.created_at).toLocaleString()}</td></tr>)}</tbody></table></div>{releases.error && <p className="banner">{releases.error}</p>}{!releases.data?.items.length && <p className="muted">暂无发布记录。</p>}</Card>
  </>
}
