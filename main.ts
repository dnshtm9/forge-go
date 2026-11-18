// The entry <-> env lifecycle, as env/src/wire.ts settles it: this Node process
// spawns the Go forge with stdio inherit and exits with its code. The Go binary
// does the whole lifecycle: bind 127.0.0.1:0, POST {"port":N} to
// $MAKEFUN_ENV_URL/register, poll GET $MAKEFUN_ENV_URL/done until {"done":true},
// exit 0. Never 8080.
//
// platform main.ts spawns forge.exe on win32 and forge-linux-amd64 elsewhere (both
// built by the same source; the linux one is committed). A missing win32 binary
// builds once from the Go sources beside this file (go is on the dev machine's
// PATH; a trial container runs linux and needs no build).

import { spawn, spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const ENV_URL = (process.env.MAKEFUN_ENV_URL ?? '').replace(/\/+$/, '');
if (ENV_URL === '') {
  process.stderr.write('[entry] MAKEFUN_ENV_URL is not set: there is no env to register with\n');
  process.exit(1);
}

const here = dirname(fileURLToPath(import.meta.url));
const bin = process.platform === 'win32' ? 'forge.exe' : 'forge-linux-amd64';
const binPath = join(here, bin);

if (!existsSync(binPath)) {
  if (process.platform !== 'win32') {
    process.stderr.write(`[entry] ${bin} is missing: the committed linux binary must ship with the sources\n`);
    process.exit(1);
  }
  const go = spawnSync('go', ['build', '-o', binPath, '.'], { cwd: here, stdio: 'inherit' });
  if (go.error !== undefined || go.status !== 0) {
    process.stderr.write(`[entry] go build failed: ${go.error !== undefined ? go.error.message : `status ${go.status}`}\n`);
    process.exit(1);
  }
}

// A git checkout can lose the exec bit (a 100644 mode in the tree, a clone on
// a filesystem that drops it): spawn then fails with EACCES and the trial
// never boots. chmod 0o755 and retry once — the fix-up is idempotent and runs
// only when a boot would otherwise die.
let retried = false;
function launch(path: string): void {
  const child = spawn(path, [], { stdio: 'inherit' });
  child.on('error', (err: NodeJS.ErrnoException) => {
    if (err.code === 'EACCES' && !retried) {
      retried = true;
      const mode = spawnSync('chmod', ['755', path], { stdio: 'ignore' });
      if (mode.error === undefined && mode.status === 0) {
        launch(path);
        return;
      }
      process.stderr.write(
        `[entry] chmod ${path} failed: ${mode.error !== undefined ? mode.error.message : `status ${mode.status}`}\n`,
      );
    }
    process.stderr.write(`[entry] ${err.message}\n`);
    process.exit(1);
  });
  child.on('exit', (code, signal) => {
    process.exit(code ?? (signal === null ? 1 : 0));
  });
}

launch(binPath);
