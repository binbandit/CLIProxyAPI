# Automatic updates from your fork

Run `scripts/auto-update.py` on the macOS or Linux machine hosting CLIProxyAPI. It watches `origin/main` every 30 seconds, downloads Go dependencies, builds incoming commits while the current server runs, and then replaces the server. Git, Python 3, Bun 1.3.14+, and Go 1.26+ must be installed. Git must have read access to your fork without interactive prompts.

From your clone, prepare `config.yaml` from `config.example.yaml` if needed, then run:

```sh
python3 scripts/auto-update.py --run-dir "$PWD"
```

Stop any existing CLIProxyAPI instance first (including a Docker container or service using the same port). The updater supervises only the server it launches; it does not stop unrelated processes. Keep the updater running in a terminal, or use your host's service manager to start this command at boot and restart it if it exits. Stop with Ctrl+C or SIGTERM to stop both the updater and its server.

Options, before the final `--`, include:

```sh
python3 scripts/auto-update.py --remote https://github.com/binbandit/CLIProxyAPI.git --branch main --interval 15 --run-dir /path/to/installation -- --config /path/to/installation/config.yaml
```

The run directory holds your config, `.env`, authentication data, logs, and other runtime files. Relative server arguments are resolved there. The updater keeps a separate disposable checkout and compiled binaries under `.auto-update/` in that directory. Use `--state-dir /another/private/folder` to override it. Do not place personal files in the state directory. Only one updater may use a state directory at a time.

Updates include every change pushed to the selected branch, including force pushes. Changes in your working clone are untouched. Failed downloads or builds leave the running server in place and retry at the next check. A new binary that exits within three seconds is replaced by the previous binary and retried on the next check. This is a process startup check, not an HTTP health check or a rollback of config/data migrations. Expect a short interruption during restart. Server output and update activity appear in the terminal.

The supervisor itself is loaded at launch: restart the updater command to pick up changes to this script. The updater also watches `https://github.com/binbandit/Cli-Proxy-API-Management-Center.git` on `main`. Override this with `--dashboard-remote URL --dashboard-branch BRANCH`. Dashboard pushes run `bun install --frozen-lockfile` and `bun run build`, then atomically install `dist/index.html` as the server's `management.html`. A failed dashboard build keeps the last working page and retries without blocking server updates. Refresh the browser after deployment to see changes; open tabs do not reload themselves.

The updater sets `MANAGEMENT_STATIC_PATH` to its managed static directory and `MANAGEMENT_STATIC_LOCAL_ONLY=true` for its server. This prevents the built-in release downloader from replacing your custom dashboard. Keep `management.disable-control-panel` false in your config. Config, credentials, and runtime files remain in the run directory. The first successful dashboard build is required before a dashboard is available. Run the updated supervisor with a server revision containing the local-only asset guard before relying on this protection; push both the script and Go changes to your backend fork first.
