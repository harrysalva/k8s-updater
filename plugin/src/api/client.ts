import type {
  ClusterInfo,
  Finding,
  RAGQueryRequest,
  RAGQueryResponse,
  Report,
} from '../types';

// Backend URL — override via UPGRADE_GUARDIAN_BACKEND env var at plugin build time
// or configure in Headlamp plugin settings.
const BACKEND_URL =
  (globalThis as Record<string, unknown>).__UPGRADE_GUARDIAN_BACKEND__ as string ??
  'http://localhost:8090';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(`${BACKEND_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...init?.headers,
    },
  });
  if (!resp.ok) {
    const body = await resp.text();
    throw new Error(`${resp.status} ${resp.statusText}: ${body}`);
  }
  return resp.json() as Promise<T>;
}

export function getClusterInfo(context?: string | null): Promise<ClusterInfo> {
  const q = context ? `?context=${encodeURIComponent(context)}` : '';
  return request<ClusterInfo>(`/api/v1/cluster${q}`);
}

export interface RunChecksOptions {
  from: string;
  to: string;
  context?: string | null;  // kubeconfig context name from Headlamp
  awsRegion?: string;
  kubesprayInventory?: string;
  kubesprayGroupVars?: string;
}

export function runChecks(opts: RunChecksOptions): Promise<Report> {
  const params = new URLSearchParams({
    from: opts.from,
    to: opts.to,
  });
  if (opts.context) params.set('context', opts.context);

  const headers: Record<string, string> = {};
  if (opts.awsRegion) headers['X-AWS-Region'] = opts.awsRegion;
  if (opts.kubesprayInventory) headers['X-Kubespray-Inventory'] = opts.kubesprayInventory;
  if (opts.kubesprayGroupVars) headers['X-Kubespray-GroupVars'] = opts.kubesprayGroupVars;

  return request<Report>(`/api/v1/check?${params}`, { headers });
}

export function installNPD(context?: string | null): Promise<NPDInstallResult> {
  const q = context ? `?context=${encodeURIComponent(context)}` : '';
  return request<NPDInstallResult>(`/api/v1/npd/install${q}`, { method: 'POST' });
}

export function ragQuery(req: RAGQueryRequest): Promise<RAGQueryResponse> {
  return request<RAGQueryResponse>('/api/v1/rag/query', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export interface NPDInstallResult {
  already_installed: boolean;
  message: string;
}

export interface ToolInfo {
  name: string;
  version: string;
  db_type: 'embedded' | 'cached' | 'runtime';
  max_k8s: string;
  cached_at?: string;
  description: string;
}

export interface CoverageWarning {
  tool: string;
  message: string;
}

export interface VersionsReport {
  tools: ToolInfo[];
  warnings?: CoverageWarning[];
}

export interface UpdateResult {
  tool: string;
  updated_at: string;
  source: string;
  max_k8s: string;
  message: string;
}

export function getVersions(target?: string): Promise<VersionsReport> {
  const q = target ? `?target=${encodeURIComponent(target)}` : '';
  return request<VersionsReport>(`/api/v1/versions${q}`);
}

export function updateVersions(): Promise<UpdateResult> {
  return request<UpdateResult>('/api/v1/versions/update', { method: 'POST' });
}

export interface DiffSummary {
  resolved_total: number;
  new_total: number;
  new_blockers: number;
  unchanged_blockers: number;
  improved: boolean;
}

export interface DiffResult {
  pre: Report;
  post: Report;
  resolved: Finding[];
  new: Finding[];
  unchanged: Finding[];
  summary: DiffSummary;
}

export interface PostCheckRequest {
  pre_report: Report;
  from: string;
  to: string;
  context?: string | null;
}

export function postCheck(req: PostCheckRequest): Promise<DiffResult> {
  return request<DiffResult>('/api/v1/postcheck', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}
