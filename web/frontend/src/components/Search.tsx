import { useEffect, useRef, useState } from 'react'
import type { Session, SearchHit } from '../types'
import type { Handlers } from '../App'
import { searchPrompts } from '../api'
import { ago } from '../format'

// Search view: full-text search across every prompt ever sent, in any session.
// Made for the many-sessions-one-directory workflow: results are told apart by
// session title, branch and tmux location — not just the project name.
export default function Search({ sessions, handlers }: { sessions: Session[]; handlers: Handlers }) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [err, setErr] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout>>()
  const byId = new Map(sessions.map(s => [s.id, s]))

  useEffect(() => {
    clearTimeout(timer.current)
    if (!q.trim()) { setHits(null); setErr(''); return }
    timer.current = setTimeout(async () => {
      try {
        const r = await searchPrompts(q.trim())
        if (Array.isArray(r)) { setHits(r); setErr('') }
        else { setHits([]); setErr((r as any).error || 'search failed') }
      } catch { setErr('search failed') }
    }, 250)
    return () => clearTimeout(timer.current)
  }, [q])

  return (
    <section className="searchview">
      <div className="controls">
        <input type="search" autoFocus placeholder='Search every prompt in every session… (FTS5: "exact phrase", AND, OR, prefix*)'
          value={q} onChange={e => setQ(e.target.value)} />
      </div>
      {err && <div className="hint">{err} — FTS5 syntax: quote phrases, e.g. "cherry-pick"</div>}
      {hits && <div className="hint">{hits.length} matching prompt{hits.length === 1 ? '' : 's'}</div>}
      <div className="searchhits">
        {(hits || []).map((h, i) => {
          const s = byId.get(h.session_id)
          return (
            <div className="shit" key={h.session_id + i}>
              <div className="shithead">
                <span className={'sdot ' + (s?.status || 'ended')} />
                <span className="proj">{h.title || s?.title || s?.ai_title || h.project || h.session_id.slice(0, 8)}</span>
                {s?.tmux_loc && <span className="tagchip">⧉ {s.tmux_loc}</span>}
                {h.git_branch && <span className="badge">{h.git_branch}</span>}
                <span className="mono dim">{h.ts ? ago(h.ts) : ''}</span>
                {s && (s.status === 'running'
                  ? <button className="btn" onClick={() => handlers.runAction('focus', s)}>focus ↗</button>
                  : <button className="btn" onClick={() => handlers.runAction('resume', s)}>resume ▶</button>)}
              </div>
              <div className="snippet"><Snippet text={h.snippet} /></div>
              <div className="cwd">{h.cwd}</div>
            </div>
          )
        })}
      </div>
      {hits && hits.length === 0 && !err && <div className="empty">No prompts match “{q}”.</div>}
      {!hits && <div className="empty">Type to search across all {sessions.length} sessions — every prompt you ever sent is indexed.</div>}
    </section>
  )
}

// Snippet renders FTS5 <mark> highlights without trusting the text as HTML —
// prompts can contain anything; only the marker tags get special treatment.
function Snippet({ text }: { text: string }) {
  const parts = text.split(/<\/?mark>/)
  return <>{parts.map((p, i) => i % 2 ? <mark key={i}>{p}</mark> : <span key={i}>{p}</span>)}</>
}
