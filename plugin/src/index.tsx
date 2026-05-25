import { registerRoute, registerSidebarEntry } from '@kinvolk/headlamp-plugin/lib';
import { Dashboard } from './components/Dashboard';

registerSidebarEntry({
  parent: 'cluster',
  name: 'upgrade-guardian',
  label: 'Upgrade Guardian',
  url: '/upgrade-guardian',
  icon: 'mdi:shield-check',
});

registerRoute({
  path: '/upgrade-guardian',
  sidebar: 'upgrade-guardian',
  name: 'Upgrade Guardian',
  exact: true,
  component: () => <Dashboard />,
});
