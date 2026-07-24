export interface Session {
  id: string
  cwd: string
  project: string
  created_at: number
  last_used_at: number
  duration_secs: number
  prompt_count: number
  message_count: number
  model: string
  git_branch: string
  first_prompt: string
  title: string
  ai_title: string
  tokens_in: number
  tokens_out: number
  current_task: string
  status: 'running' | 'ended'
  favorite: boolean
  tags: string
  notes: string
  cpu: number
  mem_mb: number
  working: boolean
  waiting: boolean
  prompt?: Prompt
  tmux_loc?: string
}

export interface PromptOption { key: string; label: string }
export interface Prompt { question: string; options: PromptOption[] }

export interface Stats {
  total: number
  running: number
  prompts: number
  projects: number
  tokens_in: number
  tokens_out: number
}

export interface Agg { key: string; sessions: number; prompts: number; tokens: number }
export interface Day { day: string; prompts: number }
export interface Analytics { daily: Day[]; topProjects: Agg[]; models: Agg[] }

export interface SkillStat { name: string; count: number; last_used: number }
export interface EnvCount { key: string; count: number }
export interface EnvStats {
  skills: SkillStat[]
  skill_runs: number
  skill_count: number
  agent_runs: number
  agent_projects: number
  agent_by_project: EnvCount[]
  agent_daily: EnvCount[]
}

export type Action = 'focus' | 'resume' | 'new' | 'kill' | 'reveal' | 'send' | 'message' | 'bypass' | 'unbypass'

// --- tmux topology ---
export interface TmuxBind { session_id?: string; pid: string; bypass: boolean; method: string }
export interface TmuxPane {
  id: string; index: number; pid: string; tty: string; cmd: string; cwd: string
  title: string; active: boolean; claude?: TmuxBind; ssh_dest?: string; nested?: TmuxTopology
}
export interface TmuxWindow { id: string; index: number; name: string; layout: string; active: boolean; panes: TmuxPane[] }
export interface TmuxSession { id: string; name: string; attached: boolean; windows: TmuxWindow[] }
export interface TmuxTopology { host: string; chain: string[] | null; sessions: TmuxSession[] | null; err?: string; as_of?: number; edge?: 'attached' | 'reachable' }

// --- full-text search ---
export interface SearchHit {
  session_id: string; ts: number; snippet: string
  project: string; cwd: string; title: string; git_branch: string
}
