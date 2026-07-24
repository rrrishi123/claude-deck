import { useEffect, useMemo, useState } from 'react'
import type { Session } from '../types'
import type { Handlers } from '../App'
import { ago, dur, fmtN, mem, isZombie } from '../format'
import RowActions from './RowActions'

type SortKey = 'project' | 'model' | 'resources' | 'prompt_count' | 'tokens' | 'last_used_at' | 'duration_secs' | 'task'

// [key, label, right-aligned]
const COLS: [SortKey, string, boolean][] = [
  ['project', 'Session', false], ['model', 'Model', false], ['resources', 'CPU / Mem', false],
  ['prompt_count', 'Prompts', true], ['tokens', 'Tokens', true], ['last_used_at', 'Last', false],
  ['duration_secs', 'Life', false], ['task', 'Task', false],
]

type Range = 'recent' | '30d' | 'all'
const WINDOW: Record<Range, number> = { recent: 7 * 864e5, '30d': 30 * 864e5, all: Infinity }
const RANGE_LABEL: Record<Range, string> = { recent: '7 days', '30d': '30 days', all: 'All time' }

function sortVal(s: Session, k: SortKey): number | string {
  if (k === 'tokens') return (s.tokens_in || 0) + (s.tokens_out || 0)
  if (k === 'resources') return (s.cpu || 0) * 1e7 + (s.mem_mb || 0)
  if (k === 'task') return (s.current_task || '').toLowerCase()
  const v = (s as any)[k]
  return typeof v === 'string' ? v.toLowerCase() : (v || 0)
}

export default function Sessions({ sessions, handlers }: { sessions: Session[]; handlers: Handlers }) {
  const [q, setQ] = useState('')
  const [filter, setFilter] = useState<'all' | 'running' | 'ended' | 'fav'>('all')
  const [range, setRange] = useState<Range>('recent')
  const [grouped, setGrouped] = useState(false)
  const [sortKey, setSortKey] = useState<SortKey>('last_used_at')
  const [sortDir, setSortDir] = useState(-1)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const list = useMemo(() => {
    const ql = q.toLowerCase().trim()
    const win = WINDOW[range]
    const now = Date.now()
    return sessions.filter(s => {
      if (filter === 'fav') { if (!s.favorite) return false }
      else if (filter !== 'all' && s.status !== filter) return false
      if (ql) {
        const hay = (s.project + ' ' + (s.title || '') + ' ' + (s.ai_title || '') + ' ' + s.cwd + ' ' + s.model + ' ' + (s.current_task || '') + ' ' + (s.first_prompt || '') + ' ' + (s.tags || '')).toLowerCase()
        return hay.includes(ql)
      }
      if (win !== Infinity && s.status !== 'running' && !s.favorite) {
        if (!s.last_used_at || now - s.last_used_at > win) return false
      }
      return true
    }).sort((a, b) => {
      const x = sortVal(a, sortKey), y = sortVal(b, sortKey)
      return (x < y ? -1 : x > y ? 1 : 0) * sortDir
    })
  }, [sessions, q, filter, sortKey, sortDir, range])

  const zombies = sessions.filter(isZombie)

  const clickSort = (k: SortKey) => {
    if (k === sortKey) setSortDir(d => -d)
    else { setSortKey(k); setSortDir(k === 'project' || k === 'model' || k === 'task' ? 1 : -1) }
  }
  const toggle = (id: string) => setExpanded(p => { const n = new Set(p); n.has(id) ? n.delete(id) : n.add(id); return n })
  const toggleGroup = (k: string) => setCollapsed(p => { const n = new Set(p); n.has(k) ? n.delete(k) : n.add(k); return n })

  const rows = () => {
    if (!grouped) return list.map(s => <Row key={s.id} s={s} open={expanded.has(s.id)} onToggle={toggle} h={handlers} />)
    const groups: Record<string, Session[]> = {}
    list.forEach(s => { (groups[s.project || '(unknown)'] ||= []).push(s) })
    const keys = Object.keys(groups).sort((a, b) => groups[b].length - groups[a].length)
    return keys.flatMap(k => {
      const g = groups[k], col = collapsed.has(k), run = g.filter(s => s.status === 'running').length
      const head = (
        <div className="mgroup" key={'g-' + k} onClick={() => toggleGroup(k)}>
          {col ? '▸' : '▾'} {k}<span className="gcount">{g.length} session{g.length > 1 ? 's' : ''}{run ? ` · ${run} running` : ''}</span>
        </div>
      )
      return col ? [head] : [head, ...g.map(s => <Row key={s.id} s={s} open={expanded.has(s.id)} onToggle={toggle} h={handlers} />)]
    })
  }

  return (
    <section>
      {zombies.length > 0 && (
        <div className="zombie">⚠ {zombies.length} zombie session{zombies.length > 1 ? 's' : ''} (running but idle &gt;2h).
          <button className="btn danger" onClick={handlers.killZombies}>✕ Kill all zombies</button>
        </div>
      )}
      <div className="controls">
        <input type="search" placeholder="Search project, path, model, task, tags…" value={q} onChange={e => setQ(e.target.value)} />
        <div className="seg">
          {(['all', 'running', 'ended', 'fav'] as const).map(f => (
            <button key={f} className={filter === f ? 'on' : ''} onClick={() => setFilter(f)}>{f === 'fav' ? '★' : f[0].toUpperCase() + f.slice(1)}</button>
          ))}
        </div>
        <div className="seg">
          {(['recent', '30d', 'all'] as const).map(r => (
            <button key={r} className={range === r ? 'on' : ''} onClick={() => setRange(r)}>{RANGE_LABEL[r]}</button>
          ))}
        </div>
        <button className={'iconbtn ' + (grouped ? 'on' : '')} onClick={() => setGrouped(g => !g)}>▦ Group by project</button>
      </div>
      {range !== 'all' && !q && (
        <div className="hint">
          Showing {list.length} of {sessions.length} — last {RANGE_LABEL[range]}, plus running &amp; pinned.
          <button className="linkbtn" onClick={() => setRange('all')}>Show all</button>
        </div>
      )}
      {q && <div className="hint">Searching all {sessions.length} sessions.</div>}

      <div className="mhead">
        {COLS.map(([k, label, r]) => (
          <div key={k} className={'mh' + (r ? ' r' : '')} onClick={() => clickSort(k)}>
            {label}{sortKey === k && <span className="arw"> {sortDir < 0 ? '▼' : '▲'}</span>}
          </div>
        ))}
        <div />
      </div>
      <div className="manifest">{rows()}</div>

      {list.length === 0 && (
        <div className="empty">
          {sessions.length === 0
            ? 'No Claude Code sessions found yet — start using Claude Code and they’ll show up here.'
            : 'No sessions match your filters.'}
        </div>
      )}
    </section>
  )
}

function Row({ s, open, onToggle, h }: { s: Session; open: boolean; onToggle: (id: string) => void; h: Handlers }) {
  const [tags, setTags] = useState<string[]>(() => (s.tags || '').split(',').map(t => t.trim()).filter(Boolean))
  const [tagInput, setTagInput] = useState('')
  const [notes, setNotes] = useState(s.notes || '')
  const [more, setMore] = useState(false)
  const [sent, setSent] = useState<string | null>(null)
  const run = s.status === 'running'
  const wk = run && s.working
  const wait = run && s.waiting && !!s.prompt
  const zomb = isZombie(s)
  useEffect(() => { if (!wait) setSent(null) }, [wait])
  const answer = (o: { key: string; label: string }) => { setSent(o.label); h.runAction('send', s, o.key) }
  const tok = (s.tokens_in || 0) + (s.tokens_out || 0)
  const model = (s.model || '').replace('claude-', '')
  const tagChips = (s.tags || '').split(',').map(t => t.trim()).filter(Boolean)
  const stop = (e: React.MouseEvent) => e.stopPropagation()
  const addTags = (raw: string) => {
    const parts = raw.split(',').map(t => t.trim()).filter(Boolean)
    if (parts.length) setTags(prev => Array.from(new Set([...prev, ...parts])))
  }
  const removeTag = (t: string) => setTags(prev => prev.filter(x => x !== t))
  const onTagKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTags(tagInput); setTagInput('') }
    else if (e.key === 'Backspace' && !tagInput && tags.length) removeTag(tags[tags.length - 1])
  }
  const saveMeta2 = (e: React.MouseEvent) => {
    stop(e)
    const finalTags = Array.from(new Set([...tags, ...tagInput.split(',').map(t => t.trim()).filter(Boolean)]))
    setTags(finalTags); setTagInput('')
    h.saveNote(s, finalTags.join(','), notes)
  }

  return (
    <>
      <div className={'mrow' + (run ? ' run' : '') + (zomb ? ' zomb' : '') + (wait ? ' need' : '')} onClick={() => onToggle(s.id)}>
        <div className="sesscell">
          <div className="sessline">
            <span className={'star ' + (s.favorite ? 'on' : '')} onClick={e => { stop(e); h.toggleFav(s) }}>{s.favorite ? '★' : '☆'}</span>
            <span className={'sdot ' + s.status + (wk ? ' working' : '') + (wait ? ' waiting' : '')} />
            <span className="proj">{s.title || s.project || '(unknown)'}</span>
            {wait && <span className="waitchip">⏳ needs you</span>}
            {wk && <span className="workchip">working<i>.</i><i>.</i><i>.</i></span>}
            {s.tmux_loc && <span className="tmuxchip" title="tmux pane">⧉ {s.tmux_loc}</span>}
            {tagChips.map(t => <span className="tagchip" key={t}>{t}</span>)}
          </div>
          <div className="cwd">{s.cwd || s.id}</div>
        </div>
        <div><span className="badge">{model || '—'}</span></div>
        <div>{run
          ? <span className={'res-cell ' + (s.cpu >= 80 ? 'hi' : '')}>
              <span className={'spark' + (wk ? ' live' : '')}><i style={{ height: '45%' }} /><i style={{ height: '75%' }} /><i style={{ height: '55%' }} /><i style={{ height: '88%' }} /></span>
              {s.cpu.toFixed(0)}% · {mem(s.mem_mb)}
            </span>
          : <span className="res-cell off">—</span>}</div>
        <div className="r mononum">{s.prompt_count || 0}</div>
        <div className="r mononum">{tok ? fmtN(tok) : '—'}</div>
        <div className="mono">{ago(s.last_used_at)}{zomb ? ' ⚠' : ''}</div>
        <div className="mono">{dur(s.duration_secs)}</div>
        <div className="task">{s.current_task || s.ai_title || s.first_prompt || '—'}</div>
        <div className="rowact"><RowActions s={s} h={h} /></div>
      </div>
      {wait && s.prompt && (
        <div className="needpanel" onClick={stop}>
          <div className="needq"><span className="needlabel">⏳ Waiting for you</span>{s.prompt.question || 'Claude is waiting for your choice.'}</div>
          {sent
            ? <div className="needsent">Sent “{sent}” ✓ · updating…</div>
            : <div className="needopts">
                {s.prompt.options.map(o => (
                  <button key={o.key} className="needopt" onClick={() => answer(o)}>
                    <b>{o.key}</b> {o.label}
                  </button>
                ))}
                <button className="needfocus" onClick={() => h.runAction('focus', s)}>Open terminal ↗</button>
              </div>}
        </div>
      )}
      {open && (
        <div className="mdetail">
          {s.current_task && <div style={{ marginBottom: 12 }}><b>Task:</b> {s.current_task}</div>}
          <div className="metabar">
            <div className="tageditor" onClick={e => { stop(e); (e.currentTarget.querySelector('input') as HTMLInputElement | null)?.focus() }}>
              {tags.map(t => (
                <span className="chip" key={t}>{t}<button onClick={e => { stop(e); removeTag(t) }}>×</button></span>
              ))}
              <input placeholder={tags.length ? 'add tag…' : 'tags — Enter or comma to add'} value={tagInput}
                onClick={stop} onChange={e => setTagInput(e.target.value)} onKeyDown={onTagKey} />
            </div>
            <textarea rows={1} placeholder="notes…" value={notes} onClick={stop} onChange={e => setNotes(e.target.value)} />
            <button className="btn" onClick={saveMeta2}>Save</button>
          </div>
          <button className="moreinfo" onClick={e => { stop(e); setMore(m => !m) }}>ⓘ Additional info {more ? '▾' : '▸'}</button>
          {more && (
            <>
              <div className="kv" style={{ marginTop: 12 }}>
                <div><b>Session</b> <span className="mono">{s.id}</span></div>
                <div><b>Created</b> {s.created_at ? new Date(s.created_at).toLocaleString() : '—'}</div>
                <div><b>Messages</b> {s.message_count || 0}</div>
                <div><b>Git branch</b> {s.git_branch || '—'}</div>
                <div><b>Tokens</b> in {fmtN(s.tokens_in || 0)} · out {fmtN(s.tokens_out || 0)}</div>
                <div><b>Status</b> {s.status}{zomb ? ' (idle zombie)' : ''}</div>
                {run && <div><b>CPU / Memory</b> {(s.cpu || 0).toFixed(1)}% · {(s.mem_mb || 0).toFixed(0)} MB</div>}
              </div>
              {s.first_prompt && <div style={{ marginTop: 8 }}><b>First prompt:</b> {s.first_prompt}</div>}
            </>
          )}
        </div>
      )}
    </>
  )
}
