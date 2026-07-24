import { useCallback, useEffect, useState } from 'react'
import type { Session, TmuxTopology, TmuxSession, TmuxPane } from '../types'
import { getTmux, tmuxExec, tmuxRestore } from '../api'
import { ago } from '../format'

// Tmux view: the live topology — every session, window and pane, with the
// Claude session running in each pane, and nested tmux servers reached
// through ssh panes (layer within layer, controllable at any depth).
export default function Tmux({ sessions, toast }: { sessions: Session[]; toast: (m: string, k?: string) => void }) {
  const [top, setTop] = useState<TmuxTopology | null>(null)
  const [follow, setFollow] = useState(false)
  const [loading, setLoading] = useState(false)

  const byId = new Map(sessions.map(s => [s.id, s]))

  const load = useCallback(async (f: boolean) => {
    setLoading(true)
    try { setTop(await getTmux(f)) } catch { toast('tmux load failed', 'err') }
    setLoading(false)
  }, [toast])

  useEffect(() => {
    load(follow)
    if (follow) return // dialing ssh hosts on a timer would be rude
    const t = setInterval(() => load(false), 8000)
    return () => clearInterval(t)
  }, [follow, load])

  const restore = async () => {
    if (!confirm('Rebuild tmux sessions from the latest snapshot? Existing sessions are left untouched.')) return
    const r = await tmuxRestore()
    toast(r.ok ? `restored: ${(r.created || []).length} created, ${(r.skipped || []).length} already there` : 'restore failed: ' + r.error, r.ok ? 'ok' : 'err')
    setTimeout(() => load(follow), 800)
  }

  return (
    <section className="tmuxview">
      <div className="controls">
        <button className={'iconbtn ' + (follow ? 'on' : '')} onClick={() => setFollow(f => !f)}>
          ⇄ Follow ssh layers{loading ? '…' : ''}
        </button>
        <button className="iconbtn" onClick={() => load(follow)}>⟳ Refresh</button>
        <button className="btn" onClick={restore}>⟲ Restore latest snapshot</button>
        <span className="hint">Snapshots are written every minute while the deck service runs — after a crash or reboot, restore brings every pane and Claude session back.</span>
      </div>
      {!top ? <div className="empty">loading…</div>
        : top.err ? <div className="empty">tmux: {top.err}</div>
        : <Layer top={top} byId={byId} toast={toast} depth={0} />}
    </section>
  )
}

function Layer({ top, byId, toast, depth }: { top: TmuxTopology; byId: Map<string, Session>; toast: (m: string, k?: string) => void; depth: number }) {
  return (
    <div className={'tmuxlayer' + (depth ? ' nested' : '')}>
      <div className="tmuxhost">
        {depth > 0 && '⇄ '}{top.host}
        {top.edge && <span className="tagchip" title="attached: this ssh pane is a live client of that tmux; reachable: it merely leads to that host">{top.edge}</span>}
        {top.as_of ? <span className="dim asof"> · as of {ago(top.as_of)}</span> : null}
        {top.err && <span className="err"> — {top.err}</span>}
      </div>
      {(top.sessions || []).map(s => <SessionTree key={s.id} s={s} top={top} byId={byId} toast={toast} depth={depth} />)}
    </div>
  )
}

function SessionTree({ s, top, byId, toast, depth }: { s: TmuxSession; top: TmuxTopology; byId: Map<string, Session>; toast: (m: string, k?: string) => void; depth: number }) {
  return (
    <div className="tmuxsess">
      <div className="tmuxsessname">▣ {s.name}{s.attached ? <span className="gcount">attached</span> : null}</div>
      {s.windows.map(w => (
        <div className="tmuxwin" key={w.id}>
          <div className="tmuxwinname">{w.index}: {w.name}{w.active ? ' •' : ''}</div>
          {w.panes.map(p => <PaneRow key={p.id} p={p} loc={`${s.name}:${w.index}.${p.index}`} top={top} byId={byId} toast={toast} depth={depth} />)}
        </div>
      ))}
    </div>
  )
}

function PaneRow({ p, loc, top, byId, toast, depth }: { p: TmuxPane; loc: string; top: TmuxTopology; byId: Map<string, Session>; toast: (m: string, k?: string) => void; depth: number }) {
  const chain = top.chain || []
  const bound = p.claude?.session_id ? byId.get(p.claude.session_id) : undefined
  const focus = async () => {
    // Selecting window+pane changes what every attached client shows — the
    // tmux-native "focus", and it works at any layer through the same chain.
    const r1 = await tmuxExec(chain, ['select-window', '-t', loc.split('.')[0]])
    const r2 = r1.ok ? await tmuxExec(chain, ['select-pane', '-t', p.id]) : r1
    toast(r2.ok ? `focused ${loc}` : 'focus failed: ' + r2.error, r2.ok ? 'ok' : 'err')
  }
  const kill = async () => {
    if (!confirm(`Kill pane ${loc} (${p.cmd})? The process running in it dies with it.`)) return
    const r = await tmuxExec(chain, ['kill-pane', '-t', p.id])
    toast(r.ok ? `killed ${loc}` : 'kill failed: ' + r.error, r.ok ? 'ok' : 'err')
  }
  return (
    <>
      <div className={'tmuxpane' + (p.claude ? ' hasclaude' : '')}>
        <span className="mono tmuxloc">{loc}</span>
        <span className="badge">{p.cmd}</span>
        {p.claude && (
          <span className="claudechip" title={`bound via ${p.claude.method}${p.claude.bypass ? ' · bypass' : ''}`}>
            ✳ {bound ? (bound.title || bound.ai_title || bound.project) : (p.claude.session_id ? p.claude.session_id.slice(0, 8) : 'claude')}
            {bound?.working ? ' · working' : ''}
          </span>
        )}
        {p.ssh_dest && <span className="tagchip">ssh → {p.ssh_dest}</span>}
        <span className="cwd">{p.cwd}</span>
        {bound && <span className="mono dim">{ago(bound.last_used_at)}</span>}
        <span className="rowact">
          <button className="btn" onClick={focus}>focus</button>
          <button className="btn danger" onClick={kill}>✕</button>
        </span>
      </div>
      {p.nested && <Layer top={p.nested} byId={byId} toast={toast} depth={depth + 1} />}
    </>
  )
}
