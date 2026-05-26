import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import Chip from '@mui/material/Chip';
import CircularProgress from '@mui/material/CircularProgress';
import Divider from '@mui/material/Divider';
import Link from '@mui/material/Link';
import Paper from '@mui/material/Paper';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import { useState } from 'react';
import { installNPD } from '../api/client';
import type { CheckResult, Finding, Severity } from '../types';

interface Props {
  result: CheckResult;
  context?: string | null;
  onRerun?: () => void;
}

const CHECKER_LABEL: Record<string, string> = {
  'deprecated-apis':        'Deprecated APIs',
  'helm-cves':              'Helm CVEs',
  'crd-schemas':            'CRD Schemas',
  'control-plane':          'Control Plane',
  'etcd-health':            'etcd Health',
  'node-health':            'Node Health',
  'provider-compatibility': 'Provider Compatibility',
  'workloads-readiness':    'Workloads Readiness',
  'webhooks-health':        'Admission Webhooks',
  'capacity-headroom':      'Capacity Headroom',
  'preflight-dryrun':       'Pre-flight Dry-run',
  'karpenter-compatibility': 'Karpenter Compatibility',
  'istio-compatibility':    'Istio Compatibility',
  'vpc-cni-version':        'VPC CNI Version',
  'subnet-ip-availability': 'Subnet IP Availability',
  'irsa-oidc':              'IRSA / OIDC Provider',
  'eks-addons':             'EKS Managed Add-ons',
};

const SEVERITY_BG: Record<Severity, string> = {
  critical: 'error.main',
  high:     'warning.main',
  medium:   'info.main',
  info:     'text.disabled',
};

const SEVERITY_ORDER: Record<Severity, number> = {
  critical: 0,
  high:     1,
  medium:   2,
  info:     3,
};

function formatMetaEntry(key: string, value: string): string {
  switch (key) {
    case 'resources_scanned':    return `${value} resources`;
    case 'kinds_checked':        return `${value} kinds`;
    case 'namespaces_checked':   return `${value} namespaces`;
    case 'removed':              return value === '0' ? '' : `${value} removed`;
    case 'deprecated':           return value === '0' ? '' : `${value} deprecated`;
    case 'releases_scanned':     return `${value} releases`;
    case 'with_constraints':     return `${value} with kubeVersion`;
    case 'incompatible':         return value === '0' ? '' : `${value} incompatible`;
    case 'crds_validated':       return `${value} CRDs`;
    case 'endpoints_checked':    return `${value} endpoints`;
    case 'nodes_checked':        return `${value} nodes`;
    case 'control_plane_nodes':  return `${value} control-plane`;
    case 'worker_nodes':         return `${value} workers`;
    case 'addons_checked':       return `${value} add-ons`;
    case 'api_version':          return `API ${value}`;
    case 'cni':                  return `CNI: ${value}`;
    case 'status':               return value.replace(/_/g, ' ');
    case 'pdbs_checked':         return `${value} PDBs`;
    case 'pdb_blockers':         return value === '0' ? '' : `${value} PDB blockers`;
    case 'deployments_checked':  return `${value} deployments`;
    case 'statefulsets_checked': return value === '0' ? '' : `${value} statefulsets`;
    case 'single_replica':       return value === '0' ? '' : `${value} single-replica`;
    case 'missing_probes':       return value === '0' ? '' : `${value} no readiness probe`;
    case 'broken_pods':          return value === '0' ? '' : `${value} broken pods`;
    // webhooks-health
    case 'validating':           return `${value} validating`;
    case 'mutating':             return `${value} mutating`;
    case 'ca_expiring_soon':     return value === '0' ? '' : `${value} CA expiring`;
    case 'unreachable':          return value === '0' ? '' : `${value} unreachable`;
    // capacity-headroom
    case 'nodes':                return `${value} nodes`;
    case 'cluster_cpu_headroom': return `CPU headroom ${value}%`;
    case 'cluster_mem_headroom': return `mem headroom ${value}%`;
    case 'worst_node_drain_fits': return value === 'true' ? 'drain fits' : 'drain DOES NOT fit';
    case 'saturated_quotas':     return value === '0' ? '' : `${value} saturated quotas`;
    case 'skip_reason':          return value;
    // preflight-dryrun
    case 'platform':             return `platform: ${value}`;
    case 'insights_checked':     return value === '0' ? '' : `${value} insights`;
    case 'errors':               return value === '0' ? '' : `${value} errors`;
    case 'warnings':             return value === '0' ? '' : `${value} warnings`;
    case 'note':                 return value;
    case 'group_vars_path':      return `inventory: ${value}`;
    case 'declared_version':     return value === '' ? '' : `declared: ${value}`;
    // eks-addons
    case 'addons_found':          return value === '0' ? 'no managed addons' : `${value} addons`;
    // irsa-oidc
    case 'oidc_issuer':           return value === 'none' ? 'no OIDC issuer' : '';
    case 'irsa_service_accounts': return value === '0' ? '' : `${value} IRSA SAs`;
    // subnet-ip-availability
    case 'subnets_checked':      return `${value} subnets`;
    case 'prefix_delegation':    return value === 'true' ? 'prefix delegation on' : '';
    // vpc-cni-version
    case 'installed_version':    return `installed: ${value}`;
    case 'minimum_required':     return `minimum: ${value}`;
    case 'default_addon_version': return `eks default: ${value}`;
    // etcd defrag
    case 'db_size_mb':           return `db: ${value} MB`;
    case 'db_size_in_use_mb':    return `in-use: ${value} MB`;
    case 'frag_pct':             return value === '0' ? '' : `fragmentation: ${value}%`;
    // karpenter / istio compatibility
    case 'installed':            return value === 'false' ? 'not installed' : 'installed';
    case 'namespace':            return `ns: ${value}`;
    case 'version':              return `version: ${value}`;
    case 'image':                return ''; // too long, hide from chips
    case 'target_k8s':           return `target: ${value}`;
    case 'supported_range':      return `supports: ${value}`;
    case 'recommended_upgrade':  return value === 'latest' || value === 'latest supported' ? '' : `recommend: ${value}`;
    case 'upstream_supported':   return value === 'true' ? '' : '⚠ EOL upstream';
    default:                     return `${key.replace(/_/g, ' ')}: ${value}`;
  }
}

function SeverityPill({ severity }: { severity: Severity }) {
  return (
    <Box
      sx={{
        px: 1,
        py: 0.25,
        borderRadius: 0.75,
        bgcolor: SEVERITY_BG[severity],
        color: 'white',
        fontSize: '0.65rem',
        fontWeight: 700,
        letterSpacing: '0.06em',
        lineHeight: 1.4,
        whiteSpace: 'nowrap',
        flexShrink: 0,
      }}
    >
      {severity.toUpperCase()}
    </Box>
  );
}

function NPDInstallButton({ context, onInstalled }: { context?: string | null; onInstalled: () => void }) {
  const [state, setState] = useState<'idle' | 'loading' | 'done' | 'error'>('idle');
  const [msg, setMsg] = useState('');

  async function handleInstall() {
    setState('loading');
    try {
      const result = await installNPD(context);
      setMsg(result.message);
      setState('done');
      setTimeout(onInstalled, 1500);
    } catch (err) {
      setMsg(String(err));
      setState('error');
    }
  }

  if (state === 'done') return <Chip label={msg} color="success" size="small" sx={{ mt: 0.5 }} />;
  if (state === 'error') return (
    <Typography variant="caption" color="error" display="block" sx={{ mt: 0.5 }}>{msg}</Typography>
  );

  return (
    <Button
      size="small"
      variant="outlined"
      color="warning"
      onClick={handleInstall}
      disabled={state === 'loading'}
      startIcon={state === 'loading' ? <CircularProgress size={12} color="inherit" /> : undefined}
      sx={{ mt: 0.5, textTransform: 'none', fontSize: '0.75rem' }}
    >
      {state === 'loading' ? 'Installing…' : 'Install node-problem-detector'}
    </Button>
  );
}

function FindingRow({ finding, context, onRerun }: { finding: Finding; context?: string | null; onRerun?: () => void }) {
  const isNPDMissing = finding.title === 'node-problem-detector is not deployed';

  return (
    <Box
      sx={{
        display: 'grid',
        gridTemplateColumns: '72px 1fr',
        gap: 1.5,
        py: 1.25,
        borderBottom: '1px solid',
        borderColor: 'divider',
        '&:last-child': { borderBottom: 'none' },
      }}
    >
      {/* Left column: severity + blocker */}
      <Stack spacing={0.5} alignItems="flex-start" pt={0.25}>
        <SeverityPill severity={finding.severity} />
        {finding.blocker && (
          <Box
            sx={{
              px: 1,
              py: 0.25,
              borderRadius: 0.75,
              border: '1px solid',
              borderColor: 'error.main',
              color: 'error.main',
              fontSize: '0.6rem',
              fontWeight: 700,
              letterSpacing: '0.06em',
              lineHeight: 1.4,
            }}
          >
            BLOCKER
          </Box>
        )}
      </Stack>

      {/* Right column: detail */}
      <Box>
        <Box sx={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 1 }}>
          <Typography variant="body2" fontWeight={600} lineHeight={1.4}>
            {finding.title}
          </Typography>
          {finding.source && (
            <Typography
              variant="caption"
              sx={{
                flexShrink: 0,
                color: 'text.disabled',
                bgcolor: 'action.hover',
                px: 0.75,
                py: 0.25,
                borderRadius: 0.5,
                fontSize: '0.65rem',
                fontFamily: 'monospace',
              }}
            >
              {finding.source}
            </Typography>
          )}
        </Box>

        {finding.resource && (
          <Typography
            variant="caption"
            sx={{ display: 'block', mt: 0.5, fontFamily: 'monospace', color: 'text.disabled' }}
          >
            {finding.resource.kind}
            {finding.resource.namespace ? `/${finding.resource.namespace}` : ''}
            /{finding.resource.name}
            {finding.resource.api_group ? ` · ${finding.resource.api_group}` : ''}
          </Typography>
        )}

        {finding.description && (
          <Typography variant="caption" color="text.secondary" display="block" sx={{ mt: 0.5, lineHeight: 1.5 }}>
            {finding.description}
          </Typography>
        )}

        {finding.remediation && !isNPDMissing && (
          <Box sx={{ mt: 0.75, display: 'flex', gap: 0.75, alignItems: 'flex-start' }}>
            <Typography variant="caption" color="success.main" fontWeight={600} sx={{ flexShrink: 0, pt: 0.1 }}>
              Fix:
            </Typography>
            <Typography variant="caption" color="success.main" sx={{ lineHeight: 1.5 }}>
              {finding.remediation}
            </Typography>
          </Box>
        )}
        {isNPDMissing && <NPDInstallButton context={context} onInstalled={() => onRerun?.()} />}

        {finding.docs_url && (
          <Box sx={{ mt: 0.5 }}>
            <Link href={finding.docs_url} target="_blank" rel="noreferrer" variant="caption">
              Documentation ↗
            </Link>
          </Box>
        )}
      </Box>
    </Box>
  );
}

export function CheckCard({ result, context, onRerun }: Props) {
  const findings = result.findings ?? [];
  const blockers = findings.filter(f => f.blocker).length;
  const total    = findings.length;

  const status: 'error' | 'blocker' | 'warn' | 'ok' | 'skip' =
    result.error   ? 'error'   :
    result.skipped ? 'skip'    :
    blockers > 0   ? 'blocker' :
    total > 0      ? 'warn'    : 'ok';

  const statusColor = {
    error:   '#f44336',
    blocker: '#f44336',
    warn:    '#ff9800',
    ok:      '#4caf50',
    skip:    '#9e9e9e',
  }[status];

  const sortedFindings = [...findings].sort((a, b) => {
    if (a.blocker !== b.blocker) return a.blocker ? -1 : 1;
    return SEVERITY_ORDER[a.severity] - SEVERITY_ORDER[b.severity];
  });

  const label = CHECKER_LABEL[result.checker_name] ?? result.checker_name;

  return (
    <Paper
      variant="outlined"
      sx={{
        mb: 1.5,
        overflow: 'hidden',
        borderColor: status === 'blocker' || status === 'error' ? 'error.main' : 'divider',
      }}
    >
      {/* Card header */}
      <Box
        sx={{
          px: 2,
          py: 1.5,
          display: 'flex',
          alignItems: 'center',
          gap: 1.5,
          bgcolor: 'action.hover',
          borderBottom: total > 0 || result.error ? '1px solid' : 'none',
          borderColor: 'divider',
        }}
      >
        {/* Status dot */}
        <Box
          sx={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            bgcolor: statusColor,
            flexShrink: 0,
            boxShadow: `0 0 0 3px ${statusColor}22`,
          }}
        />

        {/* Checker name */}
        <Typography variant="subtitle2" fontWeight={600} sx={{ flex: 1 }}>
          {label}
        </Typography>

        {/* Meta chips */}
        {result.meta && Object.entries(result.meta).filter(([k, v]) => formatMetaEntry(k, v) !== '').map(([k, v]) => (
          <Typography
            key={k}
            variant="caption"
            sx={{
              color: 'text.disabled',
              bgcolor: 'background.paper',
              border: '1px solid',
              borderColor: 'divider',
              px: 0.75,
              py: 0.25,
              borderRadius: 0.75,
              fontSize: '0.65rem',
              whiteSpace: 'nowrap',
            }}
          >
            {formatMetaEntry(k, v)}
          </Typography>
        ))}

        {/* Status badge */}
        {result.skipped && (
          <Chip label="skipped" size="small" sx={{ height: 20, fontSize: '0.65rem' }} />
        )}
        {status === 'ok' && (
          <Chip label="✓ passed" size="small" color="success" sx={{ height: 20, fontSize: '0.65rem' }} />
        )}
        {blockers > 0 && (
          <Chip
            label={`${blockers} blocker${blockers > 1 ? 's' : ''}`}
            color="error"
            size="small"
            sx={{ height: 20, fontSize: '0.65rem' }}
          />
        )}
        {status === 'warn' && blockers === 0 && (
          <Chip
            label={`${total} finding${total > 1 ? 's' : ''}`}
            color="warning"
            size="small"
            sx={{ height: 20, fontSize: '0.65rem' }}
          />
        )}
      </Box>

      {/* Error / skip reason */}
      {result.error && (
        <Box sx={{ px: 2, py: 1, bgcolor: 'error.dark' }}>
          <Typography variant="caption" color="error.contrastText">
            {result.error}
          </Typography>
        </Box>
      )}
      {result.skip_reason && (
        <Box sx={{ px: 2, py: 1 }}>
          <Typography variant="caption" color="text.disabled">
            {result.skip_reason}
          </Typography>
        </Box>
      )}

      {/* Findings */}
      {sortedFindings.length > 0 && (
        <Box sx={{ px: 2 }}>
          {sortedFindings.map(f => (
            <FindingRow
              key={f.id || `${f.checker_name}-${f.title}`}
              finding={f}
              context={context}
              onRerun={onRerun}
            />
          ))}
        </Box>
      )}
    </Paper>
  );
}
