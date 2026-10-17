#!/usr/bin/env python3
"""Install the auto-updater and its server as a macOS login service."""
import argparse
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess as sp
import sys
import time


LABEL = 'com.binbandit.cliproxyapi.updater'


def bootstrap(domain, plist):
    # launchd can still be removing a stopped registration when bootout returns.
    args = ['launchctl', 'bootstrap', domain, str(plist)]
    for attempt in range(20):
        result = sp.run(args, capture_output=True, text=True)
        if result.returncode == 0:
            return
        if result.returncode != 5 or attempt == 19:
            print(result.stderr, file=sys.stderr, end='')
            raise sp.CalledProcessError(result.returncode, args)
        time.sleep(0.25)


def service_definition(repo, run_dir, state_dir, options, label=LABEL):
    return {
        'Label': label,
        'ProgramArguments': [sys.executable, str(repo / 'scripts' / 'auto-update.py'),
                             '--run-dir', str(run_dir), '--state-dir', str(state_dir), *options],
        'WorkingDirectory': str(repo),
        'RunAtLoad': True,
        'KeepAlive': True,
        'ThrottleInterval': 5,
        'ExitTimeOut': 30,
        'StandardOutPath': str(state_dir / 'service.log'),
        'StandardErrorPath': str(state_dir / 'service.log'),
        'EnvironmentVariables': {
            'PATH': os.environ.get('PATH', '/usr/bin:/bin'),
            'GIT_TERMINAL_PROMPT': '0',
            'PYTHONUNBUFFERED': '1',
        },
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     epilog='After --, pass updater options and optional server arguments.')
    parser.add_argument('action', choices=['install', 'restart', 'status', 'uninstall'])
    parser.add_argument('--run-dir', type=Path, default=Path.cwd())
    parser.add_argument('--state-dir', type=Path)
    parser.add_argument('--label', default=LABEL, help='Existing service label to reuse, or the default')
    argv = sys.argv[1:]
    separator = argv.index('--') if '--' in argv else len(argv)
    args = parser.parse_args(argv[:separator])
    options = argv[separator + 1:]
    if not re.fullmatch(r'[A-Za-z0-9_.-]+', args.label):
        parser.error('service label may contain only letters, numbers, dots, hyphens, and underscores')
    if sys.platform != 'darwin':
        parser.error('This installer requires macOS. On Linux, run auto-update.py with your service manager.')
    repo = Path(__file__).resolve().parent.parent
    plist = Path.home() / 'Library' / 'LaunchAgents' / (args.label + '.plist')
    domain = f'gui/{os.getuid()}'
    target = domain + '/' + args.label
    if args.action == 'install':
        run_dir = args.run_dir.resolve()
        if not run_dir.is_dir():
            parser.error('run directory must exist')
        for tool in ('git', 'go', 'bun'):
            if not shutil.which(tool):
                parser.error(tool + ' must be installed and on PATH')
        state_dir = (args.state_dir or run_dir / '.auto-update').resolve()
        state_dir.mkdir(parents=True, exist_ok=True)
        plist.parent.mkdir(parents=True, exist_ok=True)
        definition = service_definition(repo, run_dir, state_dir, options, args.label)
        # Stop the prior registration before replacing its configuration.
        sp.run(['launchctl', 'bootout', target], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        temporary = plist.with_suffix('.plist.next')
        temporary.write_bytes(plistlib.dumps(definition))
        temporary.chmod(0o600)
        os.replace(temporary, plist)
        bootstrap(domain, plist)
        print(f'Updater installed and started. Logs: {state_dir / "service.log"}')
        print('Starts at login and restarts on failure. The Mac must be awake and logged in.')
    elif args.action == 'restart':
        # Gracefully stop the supervisor so it can stop its child server.
        sp.run(['launchctl', 'bootout', target], check=True)
        bootstrap(domain, plist)
    elif args.action == 'status':
        result = sp.run(['launchctl', 'print', target])
        if result.returncode:
            print('Updater service is not loaded.')
        raise SystemExit(result.returncode)
    else:
        sp.run(['launchctl', 'bootout', target], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        plist.unlink(missing_ok=True)
        print('Updater service removed. Config, credentials, and updater state retained.')


if __name__ == '__main__':
    main()
