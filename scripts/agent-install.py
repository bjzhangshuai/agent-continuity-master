#!/usr/bin/env python3
"""Detect desktop Agents and install project MCP entries with atomic backup/restore.

Defaults to a read-only plan. --apply writes only the selected workspace's config.
Adding an Agent requires a catalogue entry and a matching MCP template.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import tempfile
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
CATALOG = ROOT / 'integrations/agent-catalog.json'

def detect_paths(agent):
    paths = [p for command in agent['executables'] if (p := shutil.which(command))]
    return paths

def desktop_paths(agent):
    paths = []
    if platform.system() == 'Darwin':
        apps = {'claude':['Claude.app'], 'codebuddy':['CodeBuddy CN.app','CodeBuddy.app'], 'cursor':['Cursor.app'], 'gemini':[], 'opencode':['OpenCode.app']}
        for name in apps.get(agent['id'], []):
            for root in [Path('/Applications'),Path.home()/'Applications']:
                if (root/name).is_dir(): paths.append(str(root/name))
    return paths

def recovery_paths(workspace, agent):
    # Claude Code and CodeBuddy Code share .mcp.json. One recovery record owns
    # that file, regardless of which catalogue alias performed installation.
    key = digest(agent['config'].encode())[:24]
    return (target(workspace, '.continuity-install/' + key + '.json'),
            target(workspace, '.continuity-install/' + key + '.backup'))

def digest(raw):
    return hashlib.sha256(raw).hexdigest()

def atomic(path, raw):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.continuity-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def target(workspace, relative):
    workspace = workspace.resolve(strict=True)
    path = workspace / relative
    if Path(relative).is_absolute() or '..' in Path(relative).parts:
        raise ValueError('Config must be a relative workspace path')
    for parent in (path, *path.parents):
        if parent == workspace:
            break
        if parent.is_symlink():
            raise ValueError('Refusing symlink configuration target')
    path.resolve().relative_to(workspace)
    return path

def install(agent, workspace, executable, api_url, apply=False):
    u = urlparse(api_url)
    if u.username or u.password or u.query or u.fragment or not u.hostname or (u.scheme != 'https' and not (u.scheme == 'http' and u.hostname in ('localhost', '127.0.0.1', '::1'))):
        raise ValueError('API URL must be HTTPS or local loopback, without credentials')
    path = target(workspace, agent['config'])
    original = path.read_bytes() if path.exists() else b''
    data = json.loads(original) if original else {}
    template = json.loads((ROOT / 'integrations' / agent['template']).read_text())
    node = agent['node']
    if not isinstance(data, dict) or not isinstance(data.get(node, {}), dict):
        raise ValueError('Unsupported configuration structure')
    item = template[node]['agent-continuity']
    if node == 'mcp':
        item['command'][0] = str(executable)
        item['environment']['AGENTCLOUD_API_URL'] = api_url
    else:
        item['command'] = str(executable)
        item['env']['AGENTCLOUD_API_URL'] = api_url
    existing = data.get(node, {}).get('agent-continuity')
    if existing == item:
        return {'agent': agent['id'], 'status': 'already_installed', 'path': str(path)}
    if existing is not None:
        raise ValueError('Existing agent-continuity entry differs; preserve it and review manually')
    data.setdefault(node, {})['agent-continuity'] = item
    raw = (json.dumps(data, ensure_ascii=False, indent=2) + '\n').encode()
    result = {'agent': agent['id'], 'status': 'planned', 'path': str(path), 'capture': agent['capture'], 'transport': 'stdio MCP', 'restart_required': True}
    if apply:
        # Marker and backups never contain credentials in console output.
        state, backup = recovery_paths(workspace, agent)
        if state.exists() or backup.exists():
            raise ValueError('Existing recovery files require review before reinstall')
        atomic(backup, original)
        atomic(state, json.dumps({'config': agent['config'], 'installed_sha256': digest(raw), 'original_sha256': digest(original), 'existed': path.exists()}).encode())
        # Detect concurrent edits before committing. No vendor process is invoked.
        if (path.read_bytes() if path.exists() else b'') != original:
            raise ValueError('Configuration changed concurrently; no installation applied')
        atomic(path, raw)
        result['status'] = 'installed'
    return result

def uninstall(agent, workspace, apply=False):
    state, backup = recovery_paths(workspace, agent)
    if not state.exists():
        return {'agent': agent['id'], 'status': 'not_managed', 'path': str(target(workspace, agent['config']))}
    info = json.loads(state.read_text())
    path = target(workspace, agent['config'])
    original = backup.read_bytes()
    if info['config'] != agent['config'] or digest(path.read_bytes()) != info['installed_sha256'] or digest(original) != info['original_sha256']:
        raise ValueError('Configuration or backup changed; refusing to overwrite subsequent edits')
    if apply:
        if info['existed']:
            atomic(path, original)
        else:
            path.unlink()
        state.unlink()
        backup.unlink()
    return {'agent': agent['id'], 'status': 'restored' if apply else 'restore_planned', 'path': str(path)}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['detect', 'install', 'uninstall'])
    parser.add_argument('--agent', default='all')
    parser.add_argument('--workspace', type=Path)
    parser.add_argument('--executable', type=Path, default=ROOT/'bin/agentcloud')
    parser.add_argument('--api-url', default='http://127.0.0.1:18080')
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    catalogue = json.loads(CATALOG.read_text())
    agents = [a for a in catalogue['agents'] if args.agent in ('all', a['id'])]
    if not agents:
        raise ValueError('Unsupported Agent; inspect catalogue manual entries')
    if args.action == 'detect':
        print(json.dumps({'os': platform.system(), 'arch': platform.machine(), 'agents': [{'id': a['id'], 'name':a.get('name',a['id']), 'cli_paths': detect_paths(a), 'desktop_paths':desktop_paths(a), 'config_target':a['config'], 'capture': a['capture']} for a in agents], 'manual': catalogue['manual']}, ensure_ascii=False, indent=2))
        return
    if not args.workspace or not args.workspace.is_dir():
        raise ValueError('An existing --workspace is required')
    executable = args.executable.resolve(strict=True)
    if not executable.is_file() or not os.access(executable, os.X_OK):
        raise ValueError('--executable must be an executable agentcloud binary')
    results = []
    for agent in agents:
        try:
            result = uninstall(agent, args.workspace, args.apply) if args.action == 'uninstall' else install(agent, args.workspace, executable, args.api_url, args.apply)
        except (ValueError, OSError) as error:
            result = {'agent': agent['id'], 'status': 'blocked', 'reason': str(error)}
        results.append(result)
    print(json.dumps(results, ensure_ascii=False, indent=2))
    if any(r['status'] == 'blocked' for r in results):
        raise SystemExit(1)

if __name__ == '__main__':
    main()
