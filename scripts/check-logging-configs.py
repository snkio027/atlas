#!/usr/bin/env python3
"""Validate authored logging config with the locked binaries, without a cluster.

Images must already be present. No pulling, ports, credentials, Kubernetes
access, host log directories or runtime daemons. Docker context is explicit.
"""
import argparse
import json
import pathlib
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--docker-context', required=True)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[1]
    source = root / 'platform/development/capabilities'
    lock = json.loads((source / 'versions.lock.json').read_text())
    checks = [
        ('loki', 'loki-config', 'loki.yaml', 'docker.io/grafana/loki:',
         ['-config.file=/input/config', '-verify-config=true']),
        ('log-collection', 'alloy-config', 'config.alloy', 'docker.io/grafana/alloy:',
         ['validate', '/input/config']),
    ]
    with tempfile.TemporaryDirectory(prefix='atlas-config-check-') as scratch:
        for component, name, key, repository, command in checks:
            objects = json.loads((source / 'resources' / (component + '.json')).read_text())['items']
            config = next(o['data'][key] for o in objects if o['kind'] == 'ConfigMap' and o['metadata']['name'] == name)
            images = [image for original, image in lock['images'].items() if original.startswith(repository)]
            if len(images) != 1:
                raise ValueError('ambiguous locked image: ' + repository)
            path = pathlib.Path(scratch) / key
            path.write_text(config)
            path.chmod(0o444)  # Public configuration; temporary parent is private.
            subprocess.run([
                'docker', '--context', args.docker_context, 'run', '--rm',
                '--pull=never', '--network=none', '--read-only',
                '--cap-drop=ALL', '--security-opt=no-new-privileges',
                '--memory=256m', '--cpus=1', '--user=10001:10001',
                '--mount', 'type=bind,source=' + str(path) + ',target=/input/config,readonly',
                '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m,uid=10001,gid=10001',
                images[0], *command,
            ], check=True, timeout=60)
            print(component + ': locked config validator PASS', flush=True)


if __name__ == '__main__':
    main()
