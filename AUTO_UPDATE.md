# Automatic updates from your fork

Run `scripts/auto-update.py` on the macOS or Linux machine hosting CLIProxyAPI. It checks `origin/main` and your dashboard fork every five seconds when idle, builds incoming commits while the current server runs, and then replaces the server or dashboard. Build time is additional to the polling interval; a deployment is not instantaneous. Git, Python 3.8+, Bun 1.3.14+, and Go 1.26+ must be installed. Git must have read access to your fork without interactive prompts. Go reuses its normal module and build caches.

From your clone, prepare `config.yaml` from `config.example.yaml` if needed, then run:

```sh
python3 scripts/auto-update.py --run-dir "$PWD"
```

Stop any existing CLIProxyAPI instance first (including a Docker container or service using the same port). The updater supervises only the server it launches; it does not stop unrelated processes. Stop with Ctrl+C or SIGTERM to stop both the updater and its server.

## Background service on macOS

On the hosting Mac, after preparing your config and stopping the old instance, install the updater and the server together:

```sh
python3 scripts/service.py install --run-dir "$PWD"
python3 scripts/service.py status
```

This creates the `com.binbandit.cliproxyapi.updater` LaunchAgent. It starts immediately, starts again at login, and restarts on failure. The Mac must remain awake with the user logged in. The installer records the current tool search path; reinstall after changing runtime locations. Logs are written to `.auto-update/service.log` (rotate or prune this file as needed). Keep the source checkout at its installed location.

To reuse an existing service registration, pass `--label EXISTING_LABEL` to install and subsequent service commands. Back up its plist and disable any separate old updater before replacing that registration, so the two updaters cannot compete. Preserve its run directory and server/config arguments.

```sh
python3 scripts/service.py restart
python3 scripts/service.py uninstall
```

Uninstall stops the updater and its child server, retaining config, credentials, and state. Pass updater options after `--`; server options follow a second `--`:

```sh
python3 scripts/service.py install --run-dir /path/to/installation -- --branch main --interval 5 -- --config /path/to/installation/config.yaml
```

Use `--state-dir /another/private/folder` before the first `--` to change the state/log location. On Linux, run `auto-update.py` through your existing service manager with the required Git, Go and Bun paths. The macOS installer does not configure Linux services.

## Update behavior

Options, before the final `--`, include:

```sh
python3 scripts/auto-update.py --remote https://github.com/binbandit/CLIProxyAPI.git --branch main --interval 15 --run-dir /path/to/installation -- --config /path/to/installation/config.yaml
```

The run directory holds your config, `.env`, authentication data, logs, and other runtime files. Relative server arguments are resolved there. The updater keeps a separate disposable checkout and compiled binaries under `.auto-update/` in that directory. Use `--state-dir /another/private/folder` to override it. Do not place personal files in the state directory. Only one updater may use a state directory at a time.

Updates include every change pushed to the selected branch, including force pushes. Changes in your working clone are untouched. Failed downloads or builds leave the running server in place and retry at the next check. A new binary that exits within three seconds is replaced by the previous binary and retried on the next check. This is a process startup check, not an HTTP health check or a rollback of config/data migrations. Expect a short interruption during restart. Server output and update activity appear in the terminal.

The supervisor itself is loaded at launch: pull the updated script into the installed checkout and restart the service to pick up changes to it. The updater also watches `https://github.com/binbandit/Cli-Proxy-API-Management-Center.git` on `main`. Override this with `--dashboard-remote URL --dashboard-branch BRANCH`. Dashboard pushes reuse `node_modules` when the Bun version and tracked dependency inputs are unchanged. Missing `node_modules`, or changes to manifests, lockfiles, Bun/npm settings, or patches trigger a clean `bun install --frozen-lockfile`. Failed installs are retried. Each dashboard revision still runs `bun run build`, then atomically installs `dist/index.html` as the server's `management.html`. A failed build keeps the last working page. Dashboard and backend checks run in sequence, so a slow dashboard build delays that cycle's backend check.

Updated dashboards check for a new build every five seconds while the tab is visible. They request only the first 2 KiB of the HTML, where a build marker is embedded; no extra deployment files or management credentials are needed. A new build shows “Dashboard update ready” with **Later** and **Refresh now** actions. Save edits before refreshing. The page never reloads automatically. Tabs opened before this feature was installed need one manual refresh first.

The updater sets `MANAGEMENT_STATIC_PATH` to its managed static directory and `MANAGEMENT_STATIC_LOCAL_ONLY=true` for its server. This prevents the built-in release downloader from replacing your custom dashboard. Keep `management.disable-control-panel` false in your config. Config, credentials, and runtime files remain in the run directory. The first successful dashboard build is required before a dashboard is available. Run the updated supervisor with a server revision containing the local-only asset guard before relying on this protection; push both the script and Go changes to your backend fork first.
