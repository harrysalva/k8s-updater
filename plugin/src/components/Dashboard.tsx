import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CircularProgress from '@mui/material/CircularProgress';
import Divider from '@mui/material/Divider';
import Paper from '@mui/material/Paper';
import Stack from '@mui/material/Stack';
import TextField from '@mui/material/TextField';
import Typography from '@mui/material/Typography';
import { useEffect, useState } from 'react';
import { K8s } from '@kinvolk/headlamp-plugin/lib';
import { getClusterInfo, runChecks } from '../api/client';
import type { ClusterInfo, Report, Severity } from '../types';
import { CheckCard } from './CheckCard';
import { ClusterBadge } from './ClusterBadge';
import { DatabaseStatus } from './DatabaseStatus';
import { PostUpgradeDiff } from './PostUpgradeDiff';

const SEVERITY_ORDER: Record<Severity, number> = { critical: 0, high: 1, medium: 2, info: 3 };

interface StatTileProps {
  value: number;
  label: string;
  color: string;
}

function StatTile({ value, label, color }: StatTileProps) {
  return (
    <Paper
      variant="outlined"
      sx={{
        flex: 1,
        px: 2,
        py: 1.5,
        borderTop: '3px solid',
        borderTopColor: color,
        minWidth: 90,
      }}
    >
      <Typography variant="h5" fontWeight={700} color={color} lineHeight={1}>
        {value}
      </Typography>
      <Typography variant="caption" color="text.secondary" sx={{ mt: 0.25, display: 'block' }}>
        {label}
      </Typography>
    </Paper>
  );
}

export function Dashboard() {
  const cluster = K8s.useCluster();

  const [clusterInfo, setClusterInfo] = useState<ClusterInfo | null>(null);
  const [clusterError, setClusterError] = useState<string | null>(null);
  const [fromVersion, setFromVersion] = useState('');
  const [toVersion, setToVersion] = useState('');
  const [running, setRunning] = useState(false);
  const [report, setReport] = useState<Report | null>(null);
  const [runError, setRunError] = useState<string | null>(null);

  useEffect(() => {
    setReport(null);
    setClusterInfo(null);
    setClusterError(null);
    setFromVersion('');

    getClusterInfo(cluster)
      .then(info => {
        setClusterInfo(info);
        setFromVersion(info.version.replace(/^v/, ''));
      })
      .catch(err => setClusterError(String(err)));
  }, [cluster]);

  async function handleRun() {
    setRunning(true);
    setReport(null);
    setRunError(null);
    try {
      const r = await runChecks({ from: fromVersion, to: toVersion, context: cluster });
      setReport(r);
    } catch (err) {
      setRunError(String(err));
    } finally {
      setRunning(false);
    }
  }

  // Compute summary stats from report
  const stats = report ? (() => {
    const allFindings = report.results.flatMap(r => r.findings ?? []);
    const bySeverity: Record<Severity, number> = { critical: 0, high: 0, medium: 0, info: 0 };
    for (const f of allFindings) bySeverity[f.severity]++;
    const passed  = report.results.filter(r => !r.error && !r.skipped && (r.findings ?? []).length === 0).length;
    const skipped = report.results.filter(r => r.skipped).length;
    const failed  = report.results.filter(r => r.error).length;
    return { bySeverity, passed, skipped, failed, total: report.results.length };
  })() : null;

  // Sort results: errors first, then by finding count desc, then skipped last
  const sortedResults = report
    ? [...report.results].sort((a, b) => {
        const aBlockers = (a.findings ?? []).filter(f => f.blocker).length;
        const bBlockers = (b.findings ?? []).filter(f => f.blocker).length;
        if (aBlockers !== bBlockers) return bBlockers - aBlockers;
        if (a.error && !b.error) return -1;
        if (!a.error && b.error) return 1;

        // Sort remaining findings by worst severity
        const worstSeverity = (r: typeof a) => {
          const sev = (r.findings ?? []).map(f => SEVERITY_ORDER[f.severity]);
          return sev.length ? Math.min(...sev) : 99;
        };
        const diff = worstSeverity(a) - worstSeverity(b);
        if (diff !== 0) return diff;
        if (a.skipped && !b.skipped) return 1;
        if (!a.skipped && b.skipped) return -1;
        return 0;
      })
    : [];

  return (
    <Box sx={{ p: 3, maxWidth: 960 }}>
      {/* Page header */}
      <Box sx={{ mb: 3 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mb: 0.5 }}>
          <Typography variant="h5" fontWeight={700} letterSpacing="-0.5px">
            Upgrade Guardian
          </Typography>
          {clusterInfo && <ClusterBadge clusterType={clusterInfo.cluster_type} />}
        </Box>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5 }}>
          {cluster && (
            <Typography variant="body2" color="text.secondary" sx={{ fontFamily: 'monospace' }}>
              {cluster}
            </Typography>
          )}
          {clusterInfo && (
            <Typography variant="body2" color="text.disabled">
              {clusterInfo.version}
            </Typography>
          )}
          {clusterInfo && (
            <Typography variant="body2" color="text.disabled">
              · {clusterInfo.platform}
            </Typography>
          )}
        </Box>
      </Box>

      {clusterError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          Cannot reach backend: {clusterError}. Make sure upgrade-guardian server is running on :8090.
        </Alert>
      )}

      {/* Version selector + run */}
      <Paper variant="outlined" sx={{ p: 2, mb: 3 }}>
        <Typography variant="overline" color="text.secondary" sx={{ display: 'block', mb: 1.5, letterSpacing: '0.08em' }}>
          Upgrade path
        </Typography>
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'center', flexWrap: 'wrap' }}>
          <TextField
            label="Current version"
            value={fromVersion}
            onChange={e => setFromVersion(e.target.value)}
            size="small"
            placeholder="1.29"
            sx={{ width: 150 }}
          />
          <Typography color="text.disabled" sx={{ fontWeight: 700 }}>→</Typography>
          <TextField
            label="Target version"
            value={toVersion}
            onChange={e => setToVersion(e.target.value)}
            size="small"
            placeholder="1.30"
            sx={{ width: 150 }}
          />
          <Button
            variant="contained"
            onClick={handleRun}
            disabled={running || !fromVersion || !toVersion}
            startIcon={running ? <CircularProgress size={15} color="inherit" /> : undefined}
            sx={{ textTransform: 'none', fontWeight: 600 }}
          >
            {running ? 'Running checks…' : 'Run validation'}
          </Button>
          {report && !running && (
            <Typography variant="caption" color="text.disabled" sx={{ ml: 'auto' }}>
              {new Date(report.timestamp).toLocaleString()}
            </Typography>
          )}
        </Box>
      </Paper>

      <DatabaseStatus targetVersion={toVersion || undefined} />

      {runError && (
        <Alert severity="error" sx={{ mb: 2 }}>{runError}</Alert>
      )}

      {/* Results */}
      {report && stats && (
        <>
          {/* Overall verdict */}
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 2, mb: 2 }}>
            <Typography variant="h6" fontWeight={600}>
              {report.current_version} → {report.target_version}
            </Typography>
            <ClusterBadge clusterType={report.cluster_type} />
            <Box sx={{ ml: 'auto' }}>
              {report.blocker ? (
                <Alert severity="error" sx={{ py: 0.25, px: 1.5 }}>
                  <strong>Upgrade blocked</strong> — resolve blockers before proceeding
                </Alert>
              ) : (
                <Alert severity="success" sx={{ py: 0.25, px: 1.5 }}>
                  <strong>Safe to upgrade</strong> — no blockers found
                </Alert>
              )}
            </Box>
          </Box>

          {/* Summary tiles */}
          <Stack direction="row" spacing={1.5} sx={{ mb: 3 }}>
            <StatTile value={stats.passed}                label="passed"   color="#4caf50" />
            <StatTile value={stats.bySeverity.critical}   label="critical" color="#f44336" />
            <StatTile value={stats.bySeverity.high}       label="high"     color="#ff9800" />
            <StatTile value={stats.bySeverity.medium}     label="medium"   color="#29b6f6" />
            <StatTile value={stats.bySeverity.info}       label="info"     color="#9e9e9e" />
            {stats.skipped > 0 && (
              <StatTile value={stats.skipped} label="skipped" color="#9e9e9e" />
            )}
          </Stack>

          <Divider sx={{ mb: 2 }} />

          <PostUpgradeDiff preReport={report} context={cluster} />

          {sortedResults.map(result => (
            <CheckCard
              key={result.checker_name}
              result={result}
              context={cluster}
              onRerun={handleRun}
            />
          ))}
        </>
      )}
    </Box>
  );
}
