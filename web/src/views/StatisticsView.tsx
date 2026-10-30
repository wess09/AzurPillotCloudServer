/* Constraint brief: primary = unresolved samples and candidate image; secondary =
   classification and worker state; tertiary = hashes/version metadata. One primary
   save/publish action per view. Reuse --accent/#056dff, --text/#18181b, --panel,
   --line, --radius/8px and the existing 14px/15px type and 8/16/24px spacing. */
import { useEffect, useState } from 'react'
import { request } from '../api'
import { Card, useAction, useLoad } from '../ui'

interface ResultRow {
  sha256: string; revision: number; status: string; updated_at: string
  payload: { diagnostics: string[]; source: { metadata: { scene: string; server: string } } }
}
interface Command { id: number; action: string; state: string; result: unknown }
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
    {!rows.data?.items.length && <p className="muted">尚无重处理操作记录。模板管理请进入独立模板服务。</p>}
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
    <div className="stat-grid"><section className="card stat"><p className="stat-title">识别完成</p><p className="stat-main">{data.data?.image_counts.complete ?? 0}<small className="dim"> 张图</small></p><p className="muted">来自 {data.data?.counts.complete ?? 0} 次上报</p></section><section className="card stat"><p className="stat-title">等待补充模板</p><p className="stat-main">{missing?.images ?? 0}<small className="dim"> 张图</small></p><p className="muted">来自 {missing?.reports ?? 0} 次上报</p></section><section className="card stat"><p className="stat-title">模板管理</p><p className="stat-main">独立服务</p><button className="ghost" onClick={onClassify}>管理独立模板</button></section></div>
    <p className="muted">另有 {failed?.reports ?? 0} 次上报（{failed?.images ?? 0} 张图）解析失败，已退出模板自动重试流程。仅保留失败记录，不计入统计。</p>
    <Card title="识别节点">
      {!data.data?.workers.length && <p className="muted">尚未收到 B 的心跳。配置识别节点后，状态会显示在这里。</p>}
      {data.data?.workers.map((worker) => <div className="recognition-worker" key={worker.id}><strong>{worker.id}</strong><span className={`pill ${worker.online ? 'pill-ok' : 'pill-err'}`}>{worker.online ? '在线' : '离线'}</span><span className="muted">待处理 {worker.payload.pending} 次上报（含重识别） · 模板版本 {worker.payload.release_id}</span></div>)}
    </Card>
    <Card title={reason === 'missing_template' ? '缺少物品模板（最近 100 次上报）' : '解析失败历史（最近 100 次上报）'} extra={<div className="form-actions"><button className={reason === 'missing_template' ? '' : 'ghost'} onClick={() => setReason('missing_template')}>缺少模板（{missing?.reports ?? 0}）</button><button className={reason === 'parser_failure' ? '' : 'ghost'} onClick={() => setReason('parser_failure')}>解析失败历史（{failed?.reports ?? 0}）</button></div>}>
      {results.error && <p className="error">{results.error}</p>}
      <p className="muted">{reason === 'missing_template' ? '模板管理由独立模板服务处理。此处保留既有识别失败记录。' : '这些是识别器未能解析的历史记录，无需人工复核。发布模板不会扫描它们；识别器修复后可按需单独重试。'}</p>
      {!results.data?.items.length && <p className="muted">当前没有此类记录。</p>}
      <Feedback message={action.message} />
      {results.data?.items.map((row) => <div className="recognition-issue" key={row.sha256}><div><strong>{row.payload.source.metadata.scene} · {row.payload.source.metadata.server}</strong><p className="muted recognition-hash">{row.sha256}</p><details><summary>查看识别诊断</summary><ul>{row.payload.diagnostics.map((d) => <li key={d}>{d}</li>)}</ul></details></div><div className="form-actions">{reason === 'missing_template' && <button className="ghost" onClick={onClassify}>管理独立模板</button>}<button className="ghost" disabled={action.running} onClick={() => void action.run(async () => { const reply = await request<{ command_id: number }>('POST', '/api/v1/admin/statistics/reprocess', { ids: [row.sha256] }); return `已登记重处理 #${reply.command_id}，等待 B 接收` })}>重试此上报</button></div></div>)}
    </Card>
    <Commands />
  </>
}
