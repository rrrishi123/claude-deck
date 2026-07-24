import type { Session, Stats, Analytics, EnvStats, Action } from './types'

const j = (r: Response) => r.json()

export const getSessions = (): Promise<Session[]> => fetch('/api/sessions').then(j)
export const getStats = (): Promise<Stats> => fetch('/api/stats').then(j)
export const getAnalytics = (): Promise<Analytics> => fetch('/api/analytics').then(j)
export const getEnvironment = (): Promise<EnvStats> => fetch('/api/environment').then(j)

// getPerm returns the remembered launch-permission choice ('auto' | 'ask' |
// 'skip'), applied when starting or resuming a session. Default 'auto' sends no
// flag, so a resumed session keeps its own mode and a new one uses Claude's.
export const getPerm = (): string => localStorage.getItem('cd_perm') || 'auto'

export function doAction(action: Action, s: Session, text?: string, perm?: string): Promise<{ ok: boolean; error?: string }> {
  return fetch('/api/action', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ action, id: s.id, cwd: s.cwd, text: text || '', perm: perm ?? getPerm() }),
  }).then(j)
}

export function launchSession(cwd: string, prompt: string): Promise<{ ok: boolean; error?: string }> {
  return fetch('/api/action', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ action: 'new', id: '', cwd, text: prompt, perm: getPerm() }),
  }).then(j)
}

export function saveMeta(id: string, favorite: boolean, tags: string, notes: string) {
  return fetch('/api/meta', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ id, favorite, tags, notes }),
  }).then(j)
}

export const getTmux = (follow: boolean): Promise<import('./types').TmuxTopology> =>
  fetch('/api/tmux' + (follow ? '?follow=1' : '')).then(j)

export const tmuxExec = (chain: string[], args: string[]): Promise<{ ok: boolean; error?: string; output?: string }> =>
  fetch('/api/tmux/exec', {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ chain, args }),
  }).then(j)

export const tmuxRestore = (): Promise<{ ok: boolean; created?: string[]; skipped?: string[]; error?: string }> =>
  fetch('/api/tmux/restore', { method: 'POST', headers: { 'content-type': 'application/json' }, body: '{}' }).then(j)

export const searchPrompts = (q: string): Promise<import('./types').SearchHit[]> =>
  fetch('/api/search?q=' + encodeURIComponent(q)).then(j)
