import Box from '@mui/material/Box';
import Button from '@mui/material/Button';
import CircularProgress from '@mui/material/CircularProgress';
import Paper from '@mui/material/Paper';
import Stack from '@mui/material/Stack';
import Tooltip from '@mui/material/Tooltip';
import Typography from '@mui/material/Typography';
import { useEffect, useState } from 'react';
import { getVersions, updateVersions } from '../api/client';
import type { VersionsReport } from '../api/client';

interface Props {
  targetVersion?: string;
}

export function DatabaseStatus({ targetVersion }: Props) {
  const [report, setReport]     = useState<VersionsReport | null>(null);
  const [updating, setUpdating] = useState(false);
  const [updateMsg, setUpdateMsg] = useState('');
  const [updateErr, setUpdateErr] = useState('');

  function load() {
    getVersions(targetVersion).then(setReport).catch(() => {});
  }

  useEffect(() => { load(); }, [targetVersion]);

  async function handleUpdate() {
    setUpdating(true);
    setUpdateMsg('');
    setUpdateErr('');
    try {
      const r = await updateVersions();
      const suffix = r.source === 'module-cache' ? ' (offline — used bundled copy)' : '';
      setUpdateMsg(r.message + suffix);
      load();
    } catch (err) {
      setUpdateErr(String(err));
    } finally {
      setUpdating(false);
    }
  }

  if (!report) return null;

  const hasWarnings = (report.warnings ?? []).length > 0;

  return (
    <Paper
      variant="outlined"
      sx={{
        px: 2,
        py: 1.5,
        mb: 2,
        borderColor: hasWarnings ? 'warning.main' : 'divider',
      }}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, mb: 1 }}>
        <Typography variant="overline" color="text.secondary" sx={{ letterSpacing: '0.08em' }}>
          Tool databases
        </Typography>
        <Box sx={{ flex: 1 }} />
        <Tooltip title="Download latest Pluto versions.yaml from GitHub (no rebuild needed)">
          <span>
            <Button
              size="small"
              variant="outlined"
              onClick={handleUpdate}
              disabled={updating}
              startIcon={updating ? <CircularProgress size={12} color="inherit" /> : undefined}
              sx={{ textTransform: 'none', fontSize: '0.75rem' }}
            >
              {updating ? 'Updating…' : 'Update databases'}
            </Button>
          </span>
        </Tooltip>
      </Box>

      <Stack direction="row" spacing={2} flexWrap="wrap" useFlexGap>
        {report.tools.map(tool => (
          <Box key={tool.name} sx={{ minWidth: 120 }}>
            <Typography variant="caption" color="text.disabled" sx={{ fontFamily: 'monospace' }}>
              {tool.name}
            </Typography>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
              <Typography variant="caption" fontWeight={600}>
                {tool.max_k8s === 'n/a' || tool.max_k8s === 'current'
                  ? tool.max_k8s
                  : `≤ ${tool.max_k8s}`}
              </Typography>
              <Typography
                variant="caption"
                sx={{
                  px: 0.5,
                  borderRadius: 0.5,
                  fontSize: '0.6rem',
                  bgcolor: tool.db_type === 'cached' ? 'success.dark' : 'action.hover',
                  color: tool.db_type === 'cached' ? 'success.contrastText' : 'text.disabled',
                }}
              >
                {tool.db_type}
              </Typography>
            </Box>
            {tool.cached_at && (
              <Typography variant="caption" color="text.disabled" sx={{ fontSize: '0.6rem', display: 'block' }}>
                updated {new Date(tool.cached_at).toLocaleDateString()}
              </Typography>
            )}
          </Box>
        ))}
      </Stack>

      {/* Warnings */}
      {hasWarnings && (
        <Box sx={{ mt: 1, pt: 1, borderTop: '1px solid', borderColor: 'divider' }}>
          {report.warnings!.map((w, i) => (
            <Typography key={i} variant="caption" color="warning.main" display="block">
              ⚠ {w.message}
            </Typography>
          ))}
        </Box>
      )}

      {/* Update feedback */}
      {updateMsg && (
        <Typography variant="caption" color="success.main" display="block" sx={{ mt: 0.5 }}>
          ✓ {updateMsg}
        </Typography>
      )}
      {updateErr && (
        <Typography variant="caption" color="error" display="block" sx={{ mt: 0.5 }}>
          {updateErr}
        </Typography>
      )}
    </Paper>
  );
}
