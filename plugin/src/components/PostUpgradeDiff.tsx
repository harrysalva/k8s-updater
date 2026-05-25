import Alert from '@mui/material/Alert';
import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CircularProgress from '@mui/material/CircularProgress';
import Divider from '@mui/material/Divider';
import Paper from '@mui/material/Paper';
import Stack from '@mui/material/Stack';
import Typography from '@mui/material/Typography';
import { useState } from 'react';
import { postCheck } from '../api/client';
import type { DiffResult } from '../api/client';
import type { Finding, Report } from '../types';

interface Props {
  preReport: Report;
  context?: string | null;
}

const SEVERITY_COLOR: Record<Finding['severity'], string> = {
  critical: '#f44336',
  high:     '#ff9800',
  medium:   '#29b6f6',
  info:     '#9e9e9e',
};

function FindingLine({ f, prefix }: { f: Finding; prefix: '+' | '−' | '·' }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 1, py: 0.5 }}>
      <Typography
        sx={{
          fontFamily: 'monospace',
          fontWeight: 700,
          color: prefix === '+' ? 'error.main' : prefix === '−' ? 'success.main' : 'text.disabled',
          width: 16,
          textAlign: 'center',
          flexShrink: 0,
        }}
      >
        {prefix}
      </Typography>
      <Box
        sx={{
          width: 6,
          height: 6,
          borderRadius: '50%',
          bgcolor: SEVERITY_COLOR[f.severity],
          mt: 0.75,
          flexShrink: 0,
        }}
      />
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography variant="body2" sx={{ fontWeight: 500, lineHeight: 1.3 }}>
          {f.title}
        </Typography>
        <Typography variant="caption" color="text.disabled" sx={{ fontFamily: 'monospace' }}>
          {f.checker_name}{f.blocker ? ' · BLOCKER' : ''}
        </Typography>
      </Box>
    </Box>
  );
}

export function PostUpgradeDiff({ preReport, context }: Props) {
  const [running, setRunning] = useState(false);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [err, setErr] = useState('');

  async function handleVerify() {
    setRunning(true);
    setErr('');
    setDiff(null);
    try {
      const r = await postCheck({
        pre_report: preReport,
        from: preReport.current_version,
        to: preReport.target_version,
        context,
      });
      setDiff(r);
    } catch (e) {
      setErr(String(e));
    } finally {
      setRunning(false);
    }
  }

  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 3 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
        <Box>
          <Typography variant="overline" color="text.secondary" sx={{ letterSpacing: '0.08em' }}>
            Post-upgrade verification
          </Typography>
          <Typography variant="caption" color="text.disabled" display="block">
            Re-run all checks and compare against this report
          </Typography>
        </Box>
        <Box sx={{ flex: 1 }} />
        <Button
          variant="outlined"
          size="small"
          onClick={handleVerify}
          disabled={running}
          startIcon={running ? <CircularProgress size={14} color="inherit" /> : undefined}
          sx={{ textTransform: 'none' }}
        >
          {running ? 'Verifying…' : 'Verify post-upgrade'}
        </Button>
      </Box>

      {err && (
        <Alert severity="error" sx={{ mt: 1.5 }}>{err}</Alert>
      )}

      {diff && (
        <Box sx={{ mt: 2 }}>
          {/* Verdict */}
          {diff.summary.improved ? (
            <Alert severity="success" sx={{ mb: 2 }}>
              <strong>Upgrade verified</strong> — {diff.summary.resolved_total} findings resolved,
              no new blockers.
            </Alert>
          ) : diff.summary.new_blockers > 0 ? (
            <Alert severity="error" sx={{ mb: 2 }}>
              <strong>{diff.summary.new_blockers} new blocker(s)</strong> appeared after re-check.
              Investigate before declaring the upgrade complete.
            </Alert>
          ) : diff.summary.unchanged_blockers > 0 ? (
            <Alert severity="warning" sx={{ mb: 2 }}>
              <strong>{diff.summary.unchanged_blockers} pre-existing blocker(s)</strong> still present.
            </Alert>
          ) : (
            <Alert severity="info" sx={{ mb: 2 }}>
              No deltas vs the previous report.
            </Alert>
          )}

          {/* Counters */}
          <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
            <CounterTile label="resolved" value={diff.summary.resolved_total} color="success.main" />
            <CounterTile label="new"      value={diff.summary.new_total}      color="error.main" />
            <CounterTile label="unchanged" value={diff.unchanged.length}      color="text.disabled" />
          </Stack>

          <Divider sx={{ mb: 1 }} />

          {/* Detail sections */}
          {diff.new.length > 0 && (
            <Section title="New" findings={diff.new} prefix="+" />
          )}
          {diff.resolved.length > 0 && (
            <Section title="Resolved" findings={diff.resolved} prefix="−" />
          )}
          {diff.unchanged.length > 0 && (
            <Section title="Still present" findings={diff.unchanged.filter(f => f.blocker)} prefix="·" emptyMsg="No persisting blockers." />
          )}
        </Box>
      )}
    </Paper>
  );
}

function CounterTile({ label, value, color }: { label: string; value: number; color: string }) {
  return (
    <Box sx={{ minWidth: 80 }}>
      <Typography variant="h5" fontWeight={700} color={color} lineHeight={1}>
        {value}
      </Typography>
      <Typography variant="caption" color="text.secondary">
        {label}
      </Typography>
    </Box>
  );
}

function Section({
  title,
  findings,
  prefix,
  emptyMsg,
}: {
  title: string;
  findings: Finding[];
  prefix: '+' | '−' | '·';
  emptyMsg?: string;
}) {
  return (
    <Box sx={{ mt: 1.5 }}>
      <Typography variant="caption" color="text.secondary" sx={{ fontWeight: 600, letterSpacing: '0.06em' }}>
        {title.toUpperCase()} ({findings.length})
      </Typography>
      {findings.length === 0 && emptyMsg && (
        <Typography variant="caption" color="text.disabled" display="block">{emptyMsg}</Typography>
      )}
      {findings.map((f, i) => (
        <FindingLine key={`${title}-${i}`} f={f} prefix={prefix} />
      ))}
    </Box>
  );
}
