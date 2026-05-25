import Chip from '@mui/material/Chip';
import type { ClusterType } from '../types';

interface Props {
  clusterType: ClusterType;
}

const LABEL: Record<ClusterType, string> = {
  eks: 'EKS',
  kubespray: 'Kubespray',
  upstream: 'Upstream K8s',
  unknown: 'Unknown',
};

const COLOR: Record<ClusterType, 'primary' | 'secondary' | 'warning' | 'default'> = {
  eks: 'primary',
  kubespray: 'secondary',
  upstream: 'warning',
  unknown: 'default',
};

export function ClusterBadge({ clusterType }: Props) {
  return (
    <Chip
      label={LABEL[clusterType]}
      color={COLOR[clusterType]}
      size="small"
      sx={{ fontWeight: 700, letterSpacing: '0.05em' }}
    />
  );
}
