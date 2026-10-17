"""Offline deployment tests: real Git, supervised HTTP process, fake build tools."""
import json
import os
from pathlib import Path
import plistlib
import shutil
import socket
import subprocess as sp
import sys
import tempfile
import time
import unittest
from urllib.request import urlopen


SCRIPTS = Path(__file__).resolve().parent
UPDATER = Path(os.environ.get('AUTO_UPDATE_SCRIPT', SCRIPTS / 'auto-update.py'))


def git(directory, *args):
    return sp.check_output(['git', *args], cwd=directory, text=True, stderr=sp.DEVNULL).strip()


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.runtime = self.root / 'runtime'
        self.runtime.mkdir()
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        self.env = dict(os.environ, PATH=str(self.tools) + ':' + os.environ['PATH'],
                        TEST_DEPLOY_ROOT=str(self.root), HOME=str(self.root))

    def executable(self, name, code):
        path = self.tools / name
        path.write_text('#!' + sys.executable + '\n' + code)
        path.chmod(0o755)

    def repository(self, name):
        path = self.root / name
        path.mkdir()
        git(path, 'init', '-b', 'main')
        git(path, 'config', 'user.email', 'updater@example.invalid')
        git(path, 'config', 'user.name', 'Updater test')
        (path / 'version').write_text('one')
        (path / 'package.json').write_text('{}')
        self.commit(path)
        return path

    def commit(self, repo):
        git(repo, 'add', '.')
        git(repo, 'commit', '-m', 'fixture revision')

    def wait_for(self, predicate):
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            try:
                if predicate():
                    return
            except (OSError, ValueError):
                pass
            time.sleep(0.05)
        log = self.root / 'supervisor.log'
        self.fail(log.read_text() if log.exists() else 'Timed out waiting for fixture')

    def test_dashboard_push_reuses_dependencies_and_recovers_from_failure(self):
        backend = self.repository('backend')
        dashboard = self.repository('dashboard')
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        server = '''import http.server, os, pathlib
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write((pathlib.Path(os.environ['MANAGEMENT_STATIC_PATH'])/'management.html').read_bytes())
http.server.HTTPServer(('127.0.0.1', PORT), Handler).serve_forever()
'''.replace('PORT', str(port))
        self.executable('go', 'import sys,pathlib\nif sys.argv[1]=="build":\n'
                        ' p=pathlib.Path(sys.argv[sys.argv.index("-o")+1])\n'
                        ' p.write_text(' + repr('#!' + sys.executable + '\n' + server) + ')\n'
                        ' p.chmod(0o755)\n')
        self.executable('bun', r'''import os, pathlib, sys, time
root = pathlib.Path(os.environ['TEST_DEPLOY_ROOT'])
if sys.argv[1] == '--version':
    print('1.3.14')
elif sys.argv[1] == 'install':
    with (root/'installs').open('a') as stream: stream.write('install\n')
    if pathlib.Path('package.json').read_text() == 'bad-install': sys.exit(1)
    pathlib.Path('node_modules').mkdir(exist_ok=True)
    pathlib.Path('node_modules/kept').write_text('cached')
else:
    with (root/'builds').open('a') as stream: stream.write('build\n')
    value = pathlib.Path('version').read_text()
    if value == 'bad-build': sys.exit(1)
    if value == 'slow-build':
        (root/'builder-pid').write_text(str(os.getpid()))
        time.sleep(120)
    pathlib.Path('dist').mkdir(exist_ok=True)
    pathlib.Path('dist/index.html').write_text(value)
''')
        with (self.root / 'supervisor.log').open('w') as log:
            process = sp.Popen([sys.executable, str(UPDATER), '--remote', str(backend),
                                '--dashboard-remote', str(dashboard), '--interval', '1',
                                '--run-dir', str(self.runtime)], env=self.env, stdout=log, stderr=log)
            try:
                def page():
                    with urlopen(f'http://127.0.0.1:{port}', timeout=1) as response:
                        return response.read().decode()

                def installs():
                    return len((self.root / 'installs').read_text().splitlines())

                self.wait_for(lambda: page() == 'one')
                self.wait_for(lambda: (self.runtime / '.auto-update/revision').exists())
                self.assertEqual(installs(), 1)
                (dashboard / 'version').write_text('two')
                self.commit(dashboard)
                self.wait_for(lambda: page() == 'two')
                self.assertEqual(installs(), 1, 'A source-only push must retain installed dependencies')

                (dashboard / 'package.json').write_text('{"name":"changed"}')
                (dashboard / 'version').write_text('three')
                self.commit(dashboard)
                self.wait_for(lambda: page() == 'three')
                self.assertEqual(installs(), 2)

                (dashboard / 'package.json').write_text('bad-install')
                self.commit(dashboard)
                self.wait_for(lambda: installs() >= 4)  # Failed installs retry without a new push.
                self.assertEqual(page(), 'three')
                (dashboard / 'package.json').write_text('{"name":"changed"}')
                (dashboard / 'version').write_text('four')
                self.commit(dashboard)
                self.wait_for(lambda: page() == 'four')
                installed = installs()
                self.assertGreaterEqual(installed, 5)

                (dashboard / 'version').write_text('bad-build')
                self.commit(dashboard)
                self.wait_for(lambda: 'Dashboard update failed' in (self.root / 'supervisor.log').read_text()
                              and (self.runtime / '.auto-update/dashboard-source/version').read_text() == 'bad-build')
                self.assertEqual(page(), 'four')
                self.assertEqual(installs(), installed)
                self.assertIsNone(process.poll())

                shutil.rmtree(self.runtime / '.auto-update/dashboard-source/node_modules')
                (dashboard / 'version').write_text('five')
                self.commit(dashboard)
                self.wait_for(lambda: page() == 'five')
                self.assertEqual(installs(), installed + 1)

                (dashboard / 'version').write_text('slow-build')
                self.commit(dashboard)
                self.wait_for(lambda: (self.root / 'builder-pid').exists())
                builder_pid = int((self.root / 'builder-pid').read_text())
                process.terminate()
                process.wait(timeout=25)
                with self.assertRaises(ProcessLookupError):
                    os.kill(builder_pid, 0)
            finally:
                process.terminate()
                process.wait(timeout=25)

    @unittest.skipUnless(sys.platform == 'darwin', 'macOS service installer')
    def test_service_install_restart_and_uninstall_with_isolated_launchctl(self):
        self.executable('launchctl', '''import json,os,pathlib,sys
with (pathlib.Path(os.environ['TEST_DEPLOY_ROOT'])/'launchctl.jsonl').open('a') as out:
    print(json.dumps(sys.argv[1:]),file=out)
''')
        service = [sys.executable, str(SCRIPTS / 'service.py'), '--label', 'com.example.existing-proxy']
        sp.run([*service, 'install', '--run-dir', str(self.runtime), '--', '--interval', '7',
                '--', '--config', 'test config.yaml'], env=self.env, check=True, stdout=sp.DEVNULL)
        plist = self.root / 'Library/LaunchAgents/com.example.existing-proxy.plist'
        definition = plistlib.loads(plist.read_bytes())
        self.assertTrue(definition['KeepAlive'])
        self.assertEqual(definition['Label'], 'com.example.existing-proxy')
        self.assertEqual(definition['ProgramArguments'][-5:],
                         ['--interval', '7', '--', '--config', 'test config.yaml'])
        self.assertIn(str(self.runtime.resolve()), definition['ProgramArguments'])
        self.assertEqual(definition['EnvironmentVariables']['GIT_TERMINAL_PROMPT'], '0')
        sp.run([*service, 'restart'], env=self.env, check=True)
        sp.run([*service, 'uninstall'], env=self.env, check=True, stdout=sp.DEVNULL)
        self.assertFalse(plist.exists())
        calls = [json.loads(line)[0] for line in (self.root / 'launchctl.jsonl').read_text().splitlines()]
        self.assertEqual(calls, ['bootout', 'bootstrap', 'bootout', 'bootstrap', 'bootout'])


if __name__ == '__main__':
    unittest.main()
