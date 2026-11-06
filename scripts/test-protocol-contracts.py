#!/usr/bin/env python3
"""Validate real HTTP DTOs with JSON Schema 2020-12 (no cluster writes)."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
from jsonschema import Draft202012Validator, FormatChecker

ROOT = Path(__file__).resolve().parents[1]
with tempfile.TemporaryDirectory(prefix='continuity-contract-') as temp:
    output = Path(temp) / 'responses.json'
    env = dict(os.environ, GOMAXPROCS='2', CONTINUITY_CONTRACT_OUTPUT=str(output))
    subprocess.run(['go', 'test', '-p', '2', '-count=1', './internal/httpapi', '-run', '^TestProtocolContractAndMetering$', '-timeout', '60s'], cwd=ROOT, env=env, check=True)
    responses = json.loads(output.read_text())
    for name, schema_file in [('package', 'task-state'), ('empty_package', 'task-state'), ('usage', 'usage-record'), ('audit', 'audit-event')]:
        schema = json.loads((ROOT / f'api/schemas/{schema_file}-v1.schema.json').read_text())
        Draft202012Validator.check_schema(schema)
        validator = Draft202012Validator(schema, format_checker=FormatChecker())
        values = responses[name] if isinstance(responses[name], list) else [responses[name]]
        for value in values:
            validator.validate(value)
        # A validator accidentally accepting everything must fail this gate.
        assert not validator.is_valid({}), f'{name}: missing required fields accepted'
        print(f'PASS: {name}: {len(values)} actual HTTP/store responses and invalid envelope rejection')
