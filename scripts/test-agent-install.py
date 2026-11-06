import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('agent-install.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class InstallerTest(unittest.TestCase):
    def test_shared_claude_codebuddy_project_config(self):
        agents=json.loads(m.CATALOG.read_text())['agents'][:2]
        with tempfile.TemporaryDirectory() as folder:
            work=Path(folder)
            self.assertEqual(m.install(agents[0],work,Path('/test/agentcloud'),'http://127.0.0.1:18080',True)['status'],'installed')
            self.assertEqual(m.install(agents[1],work,Path('/test/agentcloud'),'http://127.0.0.1:18080',True)['status'],'already_installed')
            self.assertEqual(m.uninstall(agents[1],work,True)['status'],'restored')
            self.assertFalse((work/'.mcp.json').exists())
            self.assertEqual(m.uninstall(agents[0],work,True)['status'],'not_managed')
    def test_catalogue_preservation_idempotency_and_restore(self):
        for a in json.loads(m.CATALOG.read_text())['agents']:
            with self.subTest(agent=a['id']), tempfile.TemporaryDirectory() as folder:
                work = Path(folder)
                path = work/a['config']
                path.parent.mkdir(parents=True, exist_ok=True)
                original = json.dumps({a['node']: {'existing': {'command': 'preserve'}}, 'user_setting': True}).encode()
                path.write_bytes(original)
                plan = m.install(a, work, Path('/test/agentcloud'), 'http://127.0.0.1:18080')
                self.assertEqual(plan['status'], 'planned')
                self.assertEqual(path.read_bytes(), original)
                m.install(a, work, Path('/test/agentcloud'), 'http://127.0.0.1:18080', True)
                self.assertEqual(m.install(a, work, Path('/test/agentcloud'), 'http://127.0.0.1:18080', True)['status'], 'already_installed')
                self.assertEqual(json.loads(path.read_bytes())[a['node']]['existing']['command'], 'preserve')
                m.uninstall(a, work, True)
                self.assertEqual(path.read_bytes(), original)

    def test_subsequent_edits_and_symlinks_are_preserved(self):
        a = json.loads(m.CATALOG.read_text())['agents'][0]
        with tempfile.TemporaryDirectory() as folder:
            work = Path(folder)
            m.install(a, work, Path('/test/agentcloud'), 'http://127.0.0.1:18080', True)
            path = work/a['config']
            path.write_text('{"changed":true}')
            with self.assertRaises(ValueError): m.uninstall(a, work, True)
            path.unlink()
            path.symlink_to(work/'elsewhere')
            with self.assertRaises(ValueError): m.install(a, work, Path('/test/agentcloud'), 'https://example.com', True)

if __name__ == '__main__': unittest.main()
