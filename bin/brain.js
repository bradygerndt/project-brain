#!/usr/bin/env node
import { execSync, spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { load as yamlLoad, dump as yamlDump } from 'js-yaml';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const COMPOSE_FILE = resolve(ROOT, 'docker-compose.yml');

// --- helpers ---

const C = {
  reset: '\x1b[0m', bold: '\x1b[1m', dim: '\x1b[2m',
  green: '\x1b[32m', yellow: '\x1b[33m', red: '\x1b[31m',
  cyan: '\x1b[36m', magenta: '\x1b[35m',
};

const ok  = msg => console.log(`${C.green}✓${C.reset} ${msg}`);
const err = msg => console.error(`${C.red}✗${C.reset} ${msg}`);
const info = msg => console.log(`${C.cyan}→${C.reset} ${msg}`);
const dim  = msg => console.log(`${C.dim}${msg}${C.reset}`);
const bold = msg => console.log(`${C.bold}${msg}${C.reset}`);

function die(msg) { err(msg); process.exit(1); }

function compose(...args) {
  const result = spawnSync('docker', ['compose', '-f', COMPOSE_FILE, ...args], {
    cwd: ROOT, stdio: 'inherit', encoding: 'utf8',
  });
  if (result.status !== 0) process.exit(result.status ?? 1);
}

function composeMaybe(...args) {
  spawnSync('docker', ['compose', '-f', COMPOSE_FILE, ...args], {
    cwd: ROOT, stdio: 'inherit', encoding: 'utf8',
  });
}

function composeOut(...args) {
  const result = spawnSync('docker', ['compose', '-f', COMPOSE_FILE, ...args], {
    cwd: ROOT, stdio: ['inherit', 'pipe', 'pipe'], encoding: 'utf8',
  });
  return result.stdout ?? '';
}

function loadCompose() {
  return yamlLoad(readFileSync(COMPOSE_FILE, 'utf8'));
}

function saveCompose(doc) {
  writeFileSync(COMPOSE_FILE, yamlDump(doc, { lineWidth: 120, noRefs: true }));
}

function serviceName(instance) {
  return `brain-${instance}`;
}

function listInstances(doc) {
  return Object.keys(doc.services ?? {}).filter(s => s.startsWith('brain-'));
}

function mcpPort(service) {
  const ports = service.ports ?? [];
  for (const p of ports) {
    const str = String(p);
    const match = str.match(/^(\d+):\d+$/);
    if (match) return parseInt(match[1], 10);
  }
  return null;
}

async function fetchHealth(port) {
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`, { signal: AbortSignal.timeout(2000) });
    return await res.json();
  } catch {
    return null;
  }
}

// --- commands ---

const COMMANDS = {

  async start(args) {
    const instance = args[0];
    if (instance) {
      info(`Starting brain-${instance}…`);
      compose('up', '-d', serviceName(instance));
    } else {
      info('Starting all brain instances…');
      compose('up', '-d');
    }
    ok('Done. Run `brain ps` to check status.');
  },

  async stop(args) {
    const instance = args[0];
    if (instance) {
      info(`Stopping brain-${instance}…`);
      compose('stop', serviceName(instance));
    } else {
      info('Stopping all brain instances…');
      compose('stop');
    }
    ok('Stopped.');
  },

  async restart(args) {
    const instance = args[0];
    if (instance) {
      info(`Restarting brain-${instance}…`);
      compose('restart', serviceName(instance));
    } else {
      compose('restart');
    }
    ok('Restarted.');
  },

  async ps() {
    const doc = loadCompose();
    const instances = listInstances(doc);
    if (!instances.length) { info('No instances defined in docker-compose.yml'); return; }

    bold('\nBrain instances:\n');
    for (const svc of instances) {
      const name = svc.replace('brain-', '');
      const port = mcpPort(doc.services[svc]);
      const health = port ? await fetchHealth(port) : null;
      const alive = health?.ok;
      const status = alive ? `${C.green}running${C.reset}` : `${C.dim}offline${C.reset}`;
      const ui = port ? `${C.dim}http://127.0.0.1:${port}/ui${C.reset}` : '';
      const sessions = health ? ` · ${health.sessions} session${health.sessions !== 1 ? 's' : ''}` : '';
      console.log(`  ${C.magenta}${name.padEnd(16)}${C.reset} ${status}${sessions}  ${ui}`);
    }
    console.log();
  },

  async logs(args) {
    const instance = args[0];
    const follow = args.includes('-f') || args.includes('--follow');
    const logArgs = ['logs', '--tail=50'];
    if (follow) logArgs.push('-f');
    if (instance) logArgs.push(serviceName(instance));
    compose(...logArgs);
  },

  async add(args) {
    const [name, mcpPortStr, artPortStr] = args;
    if (!name || !mcpPortStr || !artPortStr) {
      die('Usage: brain add <name> <mcp-port> <artifacts-port>\n       e.g.  brain add work 3589 3590');
    }
    const mcp = parseInt(mcpPortStr, 10);
    const art = parseInt(artPortStr, 10);
    if (isNaN(mcp) || isNaN(art)) die('Ports must be integers');

    const doc = loadCompose();
    const svc = serviceName(name);
    if (doc.services?.[svc]) die(`Instance "${name}" already exists`);

    // Check port conflicts
    for (const [existing, def] of Object.entries(doc.services ?? {})) {
      for (const p of def.ports ?? []) {
        const host = parseInt(String(p).split(':')[0], 10);
        if (host === mcp || host === art) {
          die(`Port ${host} is already used by service "${existing}"`);
        }
      }
    }

    const volName = `brain-${name}-data`;
    doc.services[svc] = {
      build: '.',
      restart: 'unless-stopped',
      ports: [`${mcp}:${mcp}`, `${art}:${art}`],
      volumes: [`${volName}:/app/data`, 'hf-cache:/root/.cache/huggingface'],
      environment: {
        BRAIN_NAME: name,
        MCP_PORT: mcp,
        ARTIFACTS_PORT: art,
        ANTHROPIC_API_KEY: '${ANTHROPIC_API_KEY}',
      },
    };
    doc.volumes[volName] = null;
    saveCompose(doc);

    ok(`Added instance "${name}" (MCP :${mcp}, artifacts :${art})`);
    info('Start it with: brain start ' + name);
    info(`MCP URL: http://127.0.0.1:${mcp}/mcp`);
    info(`UI:      http://127.0.0.1:${mcp}/ui`);
  },

  async remove(args) {
    const [name] = args;
    if (!name) die('Usage: brain remove <name>');
    const doc = loadCompose();
    const svc = serviceName(name);
    if (!doc.services?.[svc]) die(`Instance "${name}" not found`);

    composeMaybe('stop', svc);
    composeMaybe('rm', '-f', svc);

    // Ask about volume
    const volName = `brain-${name}-data`;
    delete doc.services[svc];
    if (doc.volumes?.[volName] !== undefined) {
      delete doc.volumes[volName];
    }
    saveCompose(doc);

    ok(`Removed instance "${name}" from docker-compose.yml`);
    console.log(`\n${C.yellow}Note:${C.reset} The Docker volume "${volName}" still holds your data.`);
    console.log(`To permanently delete it: ${C.dim}docker volume rm project-brain_${volName}${C.reset}`);
  },

  async health(args) {
    const doc = loadCompose();
    const instances = args[0]
      ? [serviceName(args[0])]
      : listInstances(doc);

    for (const svc of instances) {
      const name = svc.replace('brain-', '');
      const port = mcpPort(doc.services[svc] ?? {});
      if (!port) { console.log(`  ${name}: no port found`); continue; }
      const h = await fetchHealth(port);
      if (h?.ok) {
        ok(`${name} (port ${port}) — ${h.sessions} active session${h.sessions !== 1 ? 's' : ''}`);
      } else {
        err(`${name} (port ${port}) — not responding`);
      }
    }
  },

  async open(args) {
    const doc = loadCompose();
    const instance = args[0] ?? listInstances(doc)[0]?.replace('brain-', '');
    if (!instance) die('No instances found');
    const svc = serviceName(instance);
    const port = mcpPort(doc.services[svc] ?? {});
    if (!port) die(`No port found for "${instance}"`);
    const url = `http://127.0.0.1:${port}/ui`;
    info(`Opening ${url}`);
    const opener = process.platform === 'win32' ? 'start' : process.platform === 'darwin' ? 'open' : 'xdg-open';
    spawnSync(opener, [url], { stdio: 'inherit', shell: true });
  },

  async config(args) {
    const doc = loadCompose();
    const instances = listInstances(doc);
    bold('\nAdd to ~/.claude/settings.json → "mcpServers":\n');
    const entries = instances.map(svc => {
      const name = svc.replace('brain-', '');
      const port = mcpPort(doc.services[svc]);
      return `    "project-brain-${name}": {\n      "type": "http",\n      "url": "http://127.0.0.1:${port}/mcp"\n    }`;
    });
    console.log(`{\n  "mcpServers": {\n${entries.join(',\n')}\n  }\n}`);
  },

  help() {
    bold('\nproject-brain CLI\n');
    const cmds = [
      ['start [name]',            'Start instance(s) — all if no name given'],
      ['stop [name]',             'Stop instance(s)'],
      ['restart [name]',          'Restart instance(s)'],
      ['ps',                      'List all instances and health status'],
      ['logs [name] [-f]',        'Show logs (follow with -f)'],
      ['add <name> <mcp> <art>',  'Add a new instance to docker-compose.yml'],
      ['remove <name>',           'Remove an instance (data volume preserved)'],
      ['health [name]',           'Hit health endpoint(s) directly'],
      ['open [name]',             'Open Web UI in browser'],
      ['config',                  'Print MCP config for ~/.claude/settings.json'],
      ['help',                    'Show this help'],
    ];
    for (const [cmd, desc] of cmds) {
      console.log(`  ${C.cyan}brain ${cmd.padEnd(26)}${C.reset}${C.dim}${desc}${C.reset}`);
    }
    console.log();
  },
};

// --- dispatch ---

const [,, cmd = 'help', ...rest] = process.argv;
const handler = COMMANDS[cmd];
if (!handler) {
  err(`Unknown command: ${cmd}`);
  COMMANDS.help();
  process.exit(1);
}
Promise.resolve(handler(rest)).catch(e => { err(e.message); process.exit(1); });
