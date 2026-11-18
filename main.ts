// The entry <-> env lifecycle, as env/src/wire.ts settles it: this Node process
// spawns the Go forge binary with stdio inherit and exits with its code. The Go
// binary does the whole lifecycle: bind 127.0.0.1:0, POST {"port":N} to
// $MAKEFUN_ENV_URL/register, poll GET $MAKEFUN_ENV_URL/done until {"done":true},
// exit 0. Never 8080.

import { spawn } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ENV_URL = (process.env.MAKEFUN_ENV_URL ?? '').replace(/\/+$/, '');
if (ENV_URL === '') {
  process.stderr.write('[entry] MAKEFUN_ENV_URL is not set: there is no env to register with\n');
  process.exit(1);
}

const here = dirname(fileURLToPath(import.meta.url));
const bin = process.platform === 'win32' ? 'forge.exe' : 'forge-linux-amd64';
const child = spawn(join(here, bin), [], { stdio: 'inherit' });

child.on('error', (err) => {
  process.stderr.write(`[entry] ${err.message}\n`);
  process.exit(1);
});
child.on('exit', (code, signal) => {
  process.exit(code ?? (signal === null ? 1 : 0));
});
