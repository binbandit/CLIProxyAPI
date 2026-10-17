#!/usr/bin/env python3
"""Watch a remote branch and supervise CLIProxyAPI (macOS/Linux)."""
import argparse
import fcntl
import os
from pathlib import Path
import signal
import shutil
import subprocess as sp
import time


def command(*args, cwd=None):
    return sp.check_output(args, cwd=cwd, text=True).strip()


def stop(process):
    if process is None or process.poll() is not None:
        return
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=20)
    except sp.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--remote', default='origin')
    parser.add_argument('--branch', default='main')
    parser.add_argument('--dashboard-remote', default='https://github.com/binbandit/Cli-Proxy-API-Management-Center.git')
    parser.add_argument('--dashboard-branch', default='main')
    parser.add_argument('--interval', type=int, default=30)
    parser.add_argument('--run-dir', type=Path, default=Path.cwd())
    parser.add_argument('--state-dir', type=Path)
    parser.add_argument('server_args', nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.interval < 1 or not args.run_dir.is_dir():
        parser.error('interval must be positive and run directory must exist')
    directory = args.run_dir.resolve()
    state = (args.state_dir or directory / '.auto-update').resolve()
    state.mkdir(parents=True, exist_ok=True)
    lock = (state / 'lock').open('w')
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        parser.error('another updater already uses this state directory')
    repo = Path(__file__).resolve().parent.parent
    remote = args.remote
    if remote in command('git', 'remote', cwd=repo).splitlines():
        remote = command('git', 'remote', 'get-url', remote, cwd=repo)
    source = state / 'source'
    if not source.exists():
        sp.run(['git', 'clone', '--no-checkout', '--', remote, str(source)], check=True)
    else:
        sp.run(['git', 'remote', 'set-url', 'origin', remote], cwd=source, check=True)
    binary, previous, candidate = (state / name for name in ('server', 'server.previous', 'server.next'))
    revision_file = state / 'revision'
    active = revision_file.read_text().strip() if revision_file.exists() and binary.exists() else None
    server_args = args.server_args
    if server_args[:1] == ['--']:
        server_args = server_args[1:]

    dashboard_source = state / 'dashboard-source'
    dashboard_revision_file = state / 'dashboard-revision'
    static = state / 'static'
    static.mkdir(exist_ok=True)
    dashboard_asset = static / 'management.html'
    dashboard_active = (dashboard_revision_file.read_text().strip()
                        if dashboard_revision_file.exists() and dashboard_asset.exists() else None)
    server_env = dict(os.environ, MANAGEMENT_STATIC_PATH=str(static),
                      MANAGEMENT_STATIC_LOCAL_ONLY='true')

    def update_dashboard():
        nonlocal dashboard_active
        if not dashboard_source.exists():
            sp.run(['git', 'clone', '--no-checkout', '--', args.dashboard_remote,
                    str(dashboard_source)], check=True)
        else:
            sp.run(['git', 'remote', 'set-url', 'origin', args.dashboard_remote],
                   cwd=dashboard_source, check=True)
        sp.run(['git', 'fetch', '--no-tags', 'origin', 'refs/heads/' + args.dashboard_branch],
               cwd=dashboard_source, check=True)
        revision = command('git', 'rev-parse', 'FETCH_HEAD', cwd=dashboard_source)
        if revision == dashboard_active:
            return
        print('Preparing dashboard', revision, flush=True)
        sp.run(['git', 'checkout', '--detach', '--force', revision], cwd=dashboard_source, check=True)
        sp.run(['git', 'clean', '-fdx'], cwd=dashboard_source, check=True)
        sp.run(['bun', 'install', '--frozen-lockfile'], cwd=dashboard_source, check=True)
        sp.run(['bun', 'run', 'build'], cwd=dashboard_source, check=True)
        built = dashboard_source / 'dist' / 'index.html'
        if not built.is_file() or built.stat().st_size == 0:
            raise RuntimeError('dashboard build did not produce dist/index.html')
        temporary = static / 'management.html.next'
        shutil.copyfile(built, temporary)
        os.replace(temporary, dashboard_asset)
        dashboard_revision_file.write_text(revision + '\n')
        dashboard_active = revision
        print('Dashboard updated; refresh your browser:', revision, flush=True)

    def launch():
        return sp.Popen([str(binary), *server_args], cwd=directory, env=server_env,
                        start_new_session=True)

    def shutdown(signum, frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    process = None
    try:
        if active:
            process = launch()
        while True:
            try:
                update_dashboard()
            except (sp.CalledProcessError, OSError, RuntimeError) as error:
                print('Dashboard update failed; keeping current page and retrying:', error, flush=True)
            try:
                sp.run(['git', 'fetch', '--no-tags', 'origin', 'refs/heads/' + args.branch], cwd=source, check=True)
                revision = command('git', 'rev-parse', 'FETCH_HEAD', cwd=source)
                if revision != active:
                    print('Preparing update', revision, flush=True)
                    sp.run(['git', 'checkout', '--detach', '--force', revision], cwd=source, check=True)
                    sp.run(['git', 'clean', '-fdx'], cwd=source, check=True)
                    sp.run(['go', 'mod', 'download'], cwd=source, check=True)
                    sp.run(['go', 'build', '-mod=readonly', '-o', str(candidate), './cmd/server'], cwd=source, check=True)
                    stop(process)
                    if binary.exists():
                        os.replace(binary, previous)
                    try:
                        os.replace(candidate, binary)
                        process = launch()
                        time.sleep(3)
                        if process.poll() is not None:
                            raise RuntimeError('updated server exited during startup')
                    except (OSError, RuntimeError):
                        stop(process)
                        if previous.exists():
                            os.replace(previous, binary)
                            process = launch()
                        raise
                    active = revision
                    revision_file.write_text(revision + '\n')
                    print('Running', revision, flush=True)
            except (sp.CalledProcessError, OSError, RuntimeError) as error:
                print('Update failed; retrying next check:', error, flush=True)
            time.sleep(args.interval)
            if process is not None and process.poll() is not None and binary.exists():
                print('Server exited; restarting', flush=True)
                process = launch()
    except KeyboardInterrupt:
        print('Stopping updater and server', flush=True)
    finally:
        stop(process)
        lock.close()


if __name__ == '__main__':
    main()
