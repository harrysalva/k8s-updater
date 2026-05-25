# Installing Upgrade Guardian

Upgrade Guardian ships as a single tarball per OS/arch. It contains:

- `bin/upgrade-guardian` — Go backend (static, no runtime dependencies)
- `plugin/main.js` — Headlamp plugin bundle (UI)
- `scripts/install.sh` — installer script
- `scripts/systemd/` and `scripts/launchd/` — service unit files
- `docs/` — this guide and `ARCHITECTURE.md`

## Requirements on the target machine

- **kubectl-ready kubeconfig** at `~/.kube/config` (or specify with `--kubeconfig`).
- **Headlamp** installed (`headlamp` desktop app or `headlamp-server` headless).
- macOS or Linux on `amd64` / `arm64`.
- Network access **not** required to upgrade Pluto's database — the bundled copy is used as a fallback.

## Quick install

```bash
tar -xzf upgrade-guardian-<version>-<os>-<arch>.tar.gz
cd upgrade-guardian-<version>-<os>-<arch>
./scripts/install.sh
```

This will:

1. Copy the binary to `/usr/local/bin/upgrade-guardian` (uses `sudo`).
2. Copy the plugin bundle to:
   - Linux: `~/.config/Headlamp/plugins/upgrade-guardian/`
   - macOS: `~/Library/Application Support/Headlamp/plugins/upgrade-guardian/`
3. Install and start a user-level service so the backend runs at login:
   - Linux: `systemd --user` unit `upgrade-guardian.service`
   - macOS: LaunchAgent `com.upgrade-guardian`

## Custom install paths

```bash
./scripts/install.sh \
  --prefix "$HOME/.local" \
  --plugin-dir "/custom/path/to/Headlamp/plugins/upgrade-guardian" \
  --no-systemd
```

## Verifying the install

```bash
# Backend health
curl http://localhost:8090/healthz       # → 200 OK

# Linux: service status
systemctl --user status upgrade-guardian

# macOS: service status
launchctl list | grep upgrade-guardian
```

Open Headlamp and check that the **Upgrade Guardian** page is in the sidebar. Plugins refresh on Headlamp restart; if you don't see it, restart Headlamp.

## Uninstalling

```bash
./scripts/install.sh --uninstall
```

This removes the binary, the plugin directory, and the user-level service.

## Manual installation (without `install.sh`)

```bash
sudo install -m 0755 bin/upgrade-guardian /usr/local/bin/
mkdir -p ~/.config/Headlamp/plugins/upgrade-guardian
cp plugin/main.js plugin/package.json ~/.config/Headlamp/plugins/upgrade-guardian/
upgrade-guardian --kubeconfig ~/.kube/config --addr 127.0.0.1:8090 &
```

## Running the backend in a different context

The plugin sends the current Headlamp context (cluster name) on each request, so the backend automatically uses the right kubeconfig context. No additional config required.

For EKS clusters, set the AWS region via header when running checks (the plugin does this when you provide it in the version selector):

```
X-AWS-Region: us-east-1
X-Cluster-Name: my-eks-cluster
```

For Kubespray clusters, point the backend at the inventory:

```
X-Kubespray-Inventory: /path/to/inventory.ini
X-Kubespray-GroupVars: /path/to/group_vars/k8s_cluster
```

## Updating

Download a newer tarball and re-run `install.sh`. It will overwrite the binary and plugin in place. Run `systemctl --user restart upgrade-guardian` (Linux) or unload/load the LaunchAgent (macOS) to pick up the new binary.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `Cannot reach backend: TypeError: Failed to fetch` in the UI | Backend not running. `systemctl --user status upgrade-guardian` or `curl localhost:8090/healthz`. |
| Plugin not visible in Headlamp | Restart Headlamp. Confirm files exist in the plugin directory. |
| `403 Forbidden` from a checker | Kubeconfig user lacks RBAC. See "Required RBAC" below. |
| EKS Insights returns "EKSConfig missing" | Provide `X-AWS-Region` header (and ensure your AWS credentials are loadable from the default chain). |
| `karpenter-compatibility` or `istio-compatibility` says `installed=false` but you have it installed | The deployment isn't in a standard namespace (Karpenter: `karpenter` or `kube-system`; Istio: `istio-system`). |

## Required RBAC

The kubeconfig user needs at minimum (all 13 checkers):

```yaml
- apiGroups: [""]
  resources: [pods, nodes, services, endpoints, resourcequotas, componentstatuses]
  verbs: [list, get]
- apiGroups: [apps]
  resources: [deployments, statefulsets, daemonsets]
  verbs: [list, get]
- apiGroups: [policy]
  resources: [poddisruptionbudgets]
  verbs: [list]
- apiGroups: [admissionregistration.k8s.io]
  resources: [validatingwebhookconfigurations, mutatingwebhookconfigurations]
  verbs: [list]
- apiGroups: [apiextensions.k8s.io]
  resources: [customresourcedefinitions]
  verbs: [list, get]
# Dynamic scan for deprecated-apis — needs list on all resources
- apiGroups: ["*"]
  resources: ["*"]
  verbs: [list]
```

For Helm releases (helm-cves) the user also needs `get` on secrets in all namespaces (Helm stores release state as secrets).
