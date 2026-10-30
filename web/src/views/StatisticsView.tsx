/* Constraint brief: primary = unresolved samples and candidate image; secondary =
   classification and worker state; tertiary = hashes/version metadata. One primary
   save/publish action per view. Reuse --accent/#056dff, --text/#18181b, --panel,
   --line, --radius/8px and the existing 14px/15px type and 8/16/24px spacing. */
import { useEffect, useState } from 'react'
import { getToken, request } from '../api'
import { Card, useAction, useLoad } from '../ui'

interface Template {
  id: string; item_id: string; name: string; category: string; rarity: number
  series: number; scene: string; enabled: boolean; image: string
}
interface Candidate {
  id: string; image: string; scene: string; status: string; occurrences: number; blocked_samples: number
  cluster_key?: string; icon_sha256?: string
  metadata: { server?: string; amount?: number; commission_name?: string; commission_status?: number; suspected_item?: string; match_score?: number }
  sources: { sample_sha256: string; file_index?: number; frame_index?: number; box?: number[] }[] | null
}
interface UnknownCluster { key: string; status: string; hits: number; icons: number; image: string }
interface ResultRow {
  sha256: string; revision: number; status: string; updated_at: string
  payload: { diagnostics: string[]; source: { metadata: { scene: string; server: string } } }
}
interface Command { id: number; action: string; state: string; result: unknown }
interface Release { id: number; created_at: string; active: boolean }
const categories: Record<string, string> = { ship_blueprint: '舰船图纸', equipment_blueprint: '装备图纸', consumable: '消耗品', other: '其他' }
const blank = (): Template => ({ id: '', item_id: '', name: '', category: 'other', rarity: 0, series: 0, scene: 'research', enabled: true, image: '' })
const list = <T,>(path: string) => request<{ items: T[] }>('GET', path)

function usePulse() {
  const [pulse, setPulse] = useState(0)
  useEffect(() => { const timer = setInterval(() => setPulse((v) => v + 1), 30000); return () => clearInterval(timer) }, [])
  return pulse
}

function Feedback({ message }: { message: { kind: 'ok' | 'err'; text: string } | null }) {
  return message ? <p role="status" className={message.kind === 'err' ? 'error' : 'muted'}>{message.text}</p> : null
}

function Commands() {
  const pulse = usePulse()
  const rows = useLoad(() => list<Command>('/api/v1/admin/worker-commands'), [pulse])
  return <Card title="试识别与重处理记录" extra={<button className="ghost" onClick={rows.reload}>刷新</button>}>
    {rows.error && <p className="error">{rows.error}</p>}
    {!rows.data?.items.length && <p className="muted">尚无操作记录。保存分类后会自动发布模板，并重跑缺少模板的上报。</p>}
    {rows.data?.items.map((cmd) => <details className="recognition-command" key={cmd.id}>
      <summary>#{cmd.id} · {cmd.action === 'test' ? '试识别' : '重新识别'} · {({ pending: '等待 B 接收', running: '正在处理', done: '已完成', failed: '处理失败' } as Record<string, string>)[cmd.state]}</summary>
      <pre>{JSON.stringify(cmd.result ?? {}, null, 2)}</pre>
    </details>)}
  </Card>
}

export function RecognitionOverview({ onClassify }: { onClassify: () => void }) {
  const pulse = usePulse()
  const [reason, setReason] = useState('missing_template')
  const data = useLoad(() => request<{ counts: Record<string, number>; image_counts: Record<string, number>; issues: Record<string, { reports: number; images: number }>; pending_candidates: number; workers: { id: string; online: boolean; payload: { release_id: number; pending: number } }[] }>('GET', '/api/v1/admin/statistics/overview'), [pulse])
  const results = useLoad(() => list<ResultRow>(`/api/v1/admin/statistics/results?status=needs_review&reason=${reason}`), [pulse, reason])
  const action = useAction()
  const missing = data.data?.issues.missing_template
  const failed = data.data?.issues.parser_failure
  return <>
    <div className="page-head"><div><h1>识别概览</h1><p className="muted">一次上报可以包含多张截图。这里只统计 B 已回传的结果，B 排队中的任务单独显示。每 30 秒刷新。</p><a href="/">查看公开统计网站 →</a></div><button className="ghost" onClick={() => { data.reload(); results.reload() }}>刷新</button></div>
    {data.error && <p className="error">{data.error}</p>}
    <div className="stat-grid"><section className="card stat"><p className="stat-title">识别完成</p><p className="stat-main">{data.data?.image_counts.complete ?? 0}<small className="dim"> 张图</small></p><p className="muted">来自 {data.data?.counts.complete ?? 0} 次上报</p></section><section className="card stat"><p className="stat-title">等待补充模板</p><p className="stat-main">{missing?.images ?? 0}<small className="dim"> 张图</small></p><p className="muted">来自 {missing?.reports ?? 0} 次上报 · 新模板发布后自动重试</p></section><section className="card stat"><p className="stat-title">待分类模板</p><p className="stat-main">{data.data?.pending_candidates ?? 0}</p><button className="ghost" onClick={onClassify}>去分类模板</button></section></div>
    <p className="muted">另有 {failed?.reports ?? 0} 次上报（{failed?.images ?? 0} 张图）解析失败，已退出模板自动重试流程。仅保留失败记录，不计入统计。</p>
    <Card title="识别节点">
      {!data.data?.workers.length && <p className="muted">尚未收到 B 的心跳。配置识别节点后，状态会显示在这里。</p>}
      {data.data?.workers.map((worker) => <div className="recognition-worker" key={worker.id}><strong>{worker.id}</strong><span className={`pill ${worker.online ? 'pill-ok' : 'pill-err'}`}>{worker.online ? '在线' : '离线'}</span><span className="muted">待处理 {worker.payload.pending} 次上报（含重识别） · 模板版本 {worker.payload.release_id}</span></div>)}
    </Card>
    <Card title={reason === 'missing_template' ? '缺少物品模板（最近 100 次上报）' : '解析失败历史（最近 100 次上报）'} extra={<div className="form-actions"><button className={reason === 'missing_template' ? '' : 'ghost'} onClick={() => setReason('missing_template')}>缺少模板（{missing?.reports ?? 0}）</button><button className={reason === 'parser_failure' ? '' : 'ghost'} onClick={() => setReason('parser_failure')}>解析失败历史（{failed?.reports ?? 0}）</button></div>}>
      {results.error && <p className="error">{results.error}</p>}
      <p className="muted">{reason === 'missing_template' ? '分类补齐模板后自动重试。此处不包含项目、数量或画面解析失败。' : '这些是识别器未能解析的历史记录，无需人工复核。发布模板不会扫描它们；识别器修复后可按需单独重试。'}</p>
      {!results.data?.items.length && <p className="muted">当前没有此类记录。</p>}
      <Feedback message={action.message} />
      {results.data?.items.map((row) => <div className="recognition-issue" key={row.sha256}><div><strong>{row.payload.source.metadata.scene} · {row.payload.source.metadata.server}</strong><p className="muted recognition-hash">{row.sha256}</p><details><summary>查看识别诊断</summary><ul>{row.payload.diagnostics.map((d) => <li key={d}>{d}</li>)}</ul></details></div><div className="form-actions">{reason === 'missing_template' && <button className="ghost" onClick={onClassify}>去分类模板</button>}<button className="ghost" disabled={action.running} onClick={() => void action.run(async () => { const reply = await request<{ command_id: number }>('POST', '/api/v1/admin/statistics/reprocess', { ids: [row.sha256] }); return `已登记重处理 #${reply.command_id}，等待 B 接收` })}>重试此上报</button></div></div>)}
    </Card>
    <Commands />
  </>
}

export function TemplateCandidates() {
  const pulse = usePulse()
  const [filter, setFilter] = useState('pending')
  const rows = useLoad(() => list<Candidate>(`/api/v1/admin/template-candidates?status=${filter}`), [filter, pulse])
  const known = useLoad(() => list<Template>('/api/v1/admin/templates'), [pulse])
  const clusters = useLoad(() => list<UnknownCluster>('/api/v1/admin/unknown-clusters'), [pulse])
  const [targetCluster, setTargetCluster] = useState('')
  const [selected, setSelected] = useState<Candidate | null>(null)
  const [form, setForm] = useState<Template>(blank)
  const [preview, setPreview] = useState('')
  const action = useAction()
  useEffect(() => () => { if (preview) URL.revokeObjectURL(preview) }, [preview])
  const choose = (candidate: Candidate) => { setSelected(candidate); setPreview(''); setTargetCluster(''); setForm(known.data?.items.find((t) => t.id === candidate.id) ?? { ...blank(), scene: 'all' }) }
  const source = selected?.sources?.[0]
  const showPreview = () => action.run(async () => {
    if (!source) throw new Error('暂无关联原图')
    const response = await fetch(`/api/v1/admin/statistics/preview/${source.sample_sha256}/${source.file_index ?? 0}?frame=${source.frame_index ?? 0}`, { headers: { Authorization: `Bearer ${getToken()}` } })
    if (!response.ok) { const body = await response.json(); throw new Error(body.error?.message ?? '原图读取失败') }
    setPreview(URL.createObjectURL(await response.blob()))
    return '预览按需从 123 盘读取，服务器不长期保存原图'
  })
  return <>
    <div className="page-head"><div><h1>待分类模板</h1><p className="muted">保存分类会自动发布模板，并重识别缺少模板的上报，无需等待下一轮派发；已解决候选自动收起，可在筛选中查看历史。</p></div><div className="form-actions"><button className="ghost" disabled={action.running} onClick={() => void action.run(async () => { const result = await request<{ queued_samples: number }>('POST', '/api/v1/admin/statistics/reprocess-unresolved'); return `已新增 ${result.queued_samples} 次重识别，已排队的任务自动合并。进度见下方记录。` })}>一键重试缺模板</button><label>筛选状态<select value={filter} onChange={(e) => setFilter(e.target.value)}><option value="pending">待分类</option><option value="assigned">已分类</option><option value="ignored">已忽略</option><option value="resolved">已解决（自动收起）</option><option value="">全部</option></select></label></div></div>
    {rows.error && <p className="error">{rows.error}</p>}
    <Feedback message={action.message} />
    {!rows.loading && !rows.data?.items.length && <Card title="当前没有候选模板"><p className="muted">B 发现匹配不到的物品后，会自动提取图标并附上来源。候选需要人工确认后才能发布。</p></Card>}
    <div className="template-layout"><div className="candidate-grid">{rows.data?.items.map((candidate) => <button className={`candidate-card ${selected?.id === candidate.id ? 'selected' : ''}`} key={candidate.id} onClick={() => choose(candidate)}><img src={`data:image/png;base64,${candidate.image}`} alt="待分类的物品图标" /><strong>{candidate.scene === 'research' ? '科研' : '通用掉落'}</strong><span>{candidate.occurrences} 次历史出现 · {candidate.blocked_samples} 份仍待处理</span><span className="muted">{candidate.metadata.server ?? ''} · {({ pending: '待分类', assigned: '已分类', ignored: '已忽略', resolved: '已解决' } as Record<string, string>)[candidate.status]}</span></button>)}</div>
      {selected && <Card title="分类与归属">
        <form className="stack" onSubmit={(e) => { e.preventDefault(); void action.run(async () => { const result = await request<{ release_id: number; queued_samples: number }>('PATCH', `/api/v1/admin/template-candidates/${selected.id}`, { status: 'assigned', template: form }); rows.reload(); known.reload(); return `分类已保存并发布版本 ${result.release_id}，新增 ${result.queued_samples} 次重识别，已有待处理任务自动合并。B 将在下一次心跳接收。` }) }}>
          <img className="candidate-large" src={`data:image/png;base64,${selected.image}`} alt="当前候选模板" />
          {selected.metadata.commission_name && <p>{selected.metadata.commission_name} · {selected.metadata.commission_status === 1 ? '大成功' : '成功'} · 数量 {selected.metadata.amount ?? '未识别'}</p>}
          {selected.cluster_key && <p className="muted">未知物品 {selected.cluster_key} · 同一物品在不同场景共用分类，发布后同步解析历史记录。</p>}
          {selected.metadata.suspected_item && <p className="muted">相近物品：{selected.metadata.suspected_item}（{((selected.metadata.match_score ?? 0) * 100).toFixed(1)}%），请对照原图确认。</p>}
          <label>物品标识<input required pattern="[A-Za-z0-9][A-Za-z0-9_. \-]{0,127}" placeholder="例如 BlueprintValparaiso" value={form.item_id} onChange={(e) => setForm({ ...form, item_id: e.target.value })} /><span className="muted">填写已有物品标识，可将不同外观归到同一物品。</span></label>
          <label>显示名称<input required value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} /></label>
          <label>物品分类<select value={form.category} onChange={(e) => setForm({ ...form, category: e.target.value })}>{Object.entries(categories).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
          <label>适用统计<select value={form.scene} onChange={(e) => setForm({ ...form, scene: e.target.value })}><option value="all">科研与通用掉落</option><option value="research">仅科研</option><option value="drop">仅通用掉落</option></select></label>
          <div className="form-row"><label>稀有度（0 表示未指定）<input type="number" min="0" max="6" value={form.rarity} onChange={(e) => setForm({ ...form, rarity: Number(e.target.value) })} /></label><label>物品所属期数（0 表示不分期）<input type="number" min="0" max="100" value={form.series} onChange={(e) => setForm({ ...form, series: Number(e.target.value) })} /></label></div>
          <p className="muted">项目期数与物品所属期数独立；物资等消耗品可以不分期。</p>
          <button type="submit" disabled={action.running}>保存分类并自动重识别</button>
          <div className="form-actions"><button type="button" className="ghost" disabled={action.running} onClick={() => void action.run(async () => { const result = await request<{ command_id: number }>('POST', '/api/v1/admin/templates/test', { ids: [selected.id] }); return `已登记试识别 #${result.command_id}，结果见下方记录` })}>试识别关联样本</button><button type="button" className="ghost" disabled={action.running} onClick={() => void action.run(async () => { await request('PATCH', `/api/v1/admin/template-candidates/${selected.id}`, { status: 'ignored', template: form }); setSelected(null); rows.reload(); return '已忽略候选' })}>忽略无效图</button><button type="button" className="ghost" disabled={action.running || !source} onClick={() => void showPreview()}>查看原始帧</button></div>
        </form>
        {selected.cluster_key && <details className="recognition-command">
          <summary className="ghost">修正未知物品归属</summary>
          <p className="muted">合并整个未知物品组，或仅拆出当前图标。保存后会发布新版本；已有原始记录保留。</p>
          <label>目标未知物品<select value={targetCluster} onChange={(e) => setTargetCluster(e.target.value)}><option value="">拆分为新的未知物品</option>{clusters.data?.items.filter((c) => c.key !== selected.cluster_key && c.status !== 'merged').map((c) => <option key={c.key} value={c.key}>{c.key} · {c.icons} 个图标 · {c.hits} 次出现</option>)}</select></label>
          <div className="form-actions">{(['merge', 'split'] as const).map((operation) => <button type="button" className="ghost" key={operation} disabled={action.running || operation === 'merge' && !targetCluster} onClick={() => void action.run(async () => {
            const result = await request<{ key: string; release_id: number }>('POST', `/api/v1/admin/unknown-clusters/${operation}`, { from_key: selected.cluster_key, to_key: targetCluster, local_key: selected.icon_sha256 ?? '' })
            setSelected(null); rows.reload(); known.reload(); clusters.reload()
            return `已保存为 ${result.key} 并发布版本 ${result.release_id}`
          })}>{operation === 'merge' ? '合并整个未知物品组' : '仅拆出当前图标'}</button>)}</div>
        </details>}
        {preview && <img className="source-preview" src={preview} alt="候选所在的原始截图帧" />}
      </Card>}
    </div>
    <Commands />
  </>
}

export function TemplateLibrary() {
  const templates = useLoad(() => list<Template>('/api/v1/admin/templates'), [])
  const releases = useLoad(() => list<Release>('/api/v1/admin/template-releases'), [])
  const action = useAction()
  return <>
    <div className="page-head"><div><h1>模板库</h1><p className="muted">管理在线补充模板。发布后，B 同步版本并重跑关联样本。</p></div><button disabled={action.running || !templates.data?.items.length} onClick={() => void action.run(async () => { const result = await request<{ release_id: number }>('POST', '/api/v1/admin/templates/publish'); releases.reload(); return `已发布版本 ${result.release_id}，等待 B 同步` })}>发布并重识别</button></div>
    <Feedback message={action.message} />
    {templates.error && <p className="error">{templates.error}</p>}
    <Card title="已分类模板"><div className="table-wrap"><table><thead><tr><th>模板</th><th>物品</th><th>分类</th><th>期数 / 稀有度</th><th>状态</th><th>操作</th></tr></thead><tbody>{templates.data?.items.map((t) => <tr key={t.id}><td><img className="template-thumb" src={`data:image/png;base64,${t.image}`} alt={t.name} /></td><td><strong>{t.name}</strong><div className="muted">{t.item_id}</div></td><td>{categories[t.category]}</td><td>{t.series || '不分期'} / {t.rarity || '未指定'}</td><td>{t.enabled ? '启用' : '禁用'}</td><td><button className="ghost" disabled={action.running} onClick={() => void action.run(async () => { await request('PATCH', `/api/v1/admin/template-candidates/${t.id}`, { status: 'assigned', template: { ...t, enabled: !t.enabled } }); templates.reload(); return '已保存并自动发布，B 同步后重识别相关记录' })}>{t.enabled ? '禁用' : '启用'}</button></td></tr>)}</tbody></table></div>{!templates.data?.items.length && <p className="muted">尚无在线补充模板。到“待分类模板”确认候选后，会显示在这里。B 的基础模板仍来自 AP 识别资源。</p>}</Card>
    <Card title="发布与回滚记录">{releases.error && <p className="error">{releases.error}</p>}{!releases.data?.items.length && <p className="muted">尚未发布模板版本。</p>}{releases.data?.items.map((release) => <div className="recognition-worker" key={release.id}><strong>版本 {release.id}</strong><span className="muted">{new Date(release.created_at).toLocaleString()}</span>{release.active ? <span className="pill pill-ok">当前版本</span> : <button className="ghost" disabled={action.running} onClick={() => void action.run(async () => { const result = await request<{ release_id: number }>('POST', '/api/v1/admin/templates/rollback', { release_id: release.id }); releases.reload(); return `已创建回滚版本 ${result.release_id}，恢复版本 ${release.id} 的模板内容` })}>恢复此版本</button>}</div>)}</Card>
    <Commands />
  </>
}
