export type ClusterType = 'eks' | 'kubespray' | 'upstream' | 'unknown';
export type Severity = 'critical' | 'high' | 'medium' | 'info';

export interface Resource {
  kind: string;
  name: string;
  namespace?: string;
  api_group?: string;
}

export interface Finding {
  id: string;
  checker_name: string;
  cluster_type: ClusterType;
  severity: Severity;
  blocker: boolean;
  title: string;
  description: string;
  remediation: string;
  resource?: Resource;
  source: string;
  docs_url?: string;
}

export interface CheckResult {
  checker_name: string;
  findings: Finding[];
  meta?: Record<string, string>;
  error?: string;
  skipped?: boolean;
  skip_reason?: string;
}

export interface Report {
  cluster_type: ClusterType;
  current_version: string;
  target_version: string;
  results: CheckResult[];
  timestamp: string;
  blocker: boolean;
}

export interface ClusterInfo {
  cluster_type: ClusterType;
  version: string;
  platform: string;
}

export interface RAGQueryRequest {
  query: string;
  provider: string;
  version_range?: string;
}

export interface RAGQueryResponse {
  explanation: string;
  sources: string[];
}
