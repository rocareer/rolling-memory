#!/usr/bin/env node
// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Rolling-Memory MCP launcher (cross-platform).
//
// Resolves the Rolling-Memory binary and execs it as `mcp`, mirroring
// bin/rolling-memory-lib.sh. Used by the CodeBuddy / WorkBuddy plugin bundle
// where a static command must work on every desktop platform: Node is the one
// runtime those hosts manage themselves, while POSIX sh is absent on Windows.
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const https = require('https');
const crypto = require('crypto');
const { spawnSync } = require('child_process');

const DEFAULT_RELEASE_BASE = 'https://github.com/rocareer/rolling-memory/releases/latest/download';
// No default mirror: GitHub is the single public source for this project.
// Inject a mirror explicitly with ROLLING_MEMORY_RELEASE_MIRROR.
const DEFAULT_RELEASE_MIRROR = '';

function osName() {
  switch (process.platform) {
    case 'darwin': return 'darwin';
    case 'win32': return 'windows';
    case 'freebsd': return 'freebsd';
    default: return 'linux';
  }
}

function archName() {
  switch (process.arch) {
    case 'x64': return 'amd64';
    case 'arm64': return 'arm64';
    default: return process.arch;
  }
}

function dataDir(home) {
  if (process.platform === 'win32') {
    return path.join(process.env.LOCALAPPDATA || path.join(home, 'AppData', 'Local'), 'rolling-memory');
  }
  return path.join(process.env.XDG_DATA_HOME || path.join(home, '.local', 'share'), 'rolling-memory');
}

// candidateCommands lists the exec sizes accepted for each candidate; the POSIX
// sh shims bootstraps and are skipped on Windows.
function candidateCommands(home) {
  const exe = process.platform === 'win32' ? '.exe' : '';
  const binDir = path.join(dataDir(home), 'bin');
  const candidates = [];
  if (process.env.ROLLING_MEMORY_BIN) {
    candidates.push(process.env.ROLLING_MEMORY_BIN);
  }
  if (process.platform !== 'win32') {
    const pluginRoot = process.env.CODEBUDDY_PLUGIN_ROOT || process.env.CLAUDE_PLUGIN_ROOT;
    if (pluginRoot) {
      candidates.push(path.join(pluginRoot, 'bin', 'rolling-memory-mcp'));
    }
    candidates.push(path.join(binDir, 'rolling-memory.sh'));
  }
  candidates.push(path.join(binDir, 'rolling-memory' + exe));
  candidates.push(path.join(home, '.local', 'bin', 'rolling-memory' + exe));
  return candidates;
}

function execCommand(command, args) {
  if (process.platform === 'win32' && /\.(cmd|bat)$/i.test(command)) {
    return spawnSync('cmd', ['/c', command, ...args], { stdio: 'inherit' });
  }
  return spawnSync(command, args, { stdio: 'inherit' });
}

function fetchBuffer(url, redirectsLeft = 5) {
  return new Promise((resolve, reject) => {
    if (redirectsLeft < 0) {
      reject(new Error('too many redirects for ' + url));
      return;
    }
    const client = url.startsWith('https:') ? https : http;
    const req = client.get(url, (res) => {
      if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location) {
        res.resume();
        let nextUrl = res.headers.location;
        try {
          nextUrl = new URL(nextUrl, url).toString();
        } catch (err) {
          reject(err);
          return;
        }
        resolve(fetchBuffer(nextUrl, redirectsLeft - 1));
        return;
      }
      if (res.statusCode !== 200) {
        res.resume();
        reject(new Error('HTTP ' + res.statusCode + ' for ' + url));
        return;
      }
      const chunks = [];
      res.on('data', (chunk) => chunks.push(chunk));
      res.on('end', () => resolve(Buffer.concat(chunks)));
    });
    req.setTimeout(60000, () => {
      req.destroy(new Error('request timeout for ' + url));
    });
    req.on('error', reject);
  });
}

function fetchText(url, redirectsLeft = 5) {
  return fetchBuffer(url, redirectsLeft).then((buf) => buf.toString('utf8'));
}

// bootstrap downloads the platform asset from the release channel, verifies it
// against SHA256SUMS, and installs it atomically next to the launcher targets.
async function bootstrap(home, base) {
  const exe = process.platform === 'win32' ? '.exe' : '';
  const asset = 'rolling-memory-' + osName() + '-' + archName() + exe;
  const sums = await fetchText(base.replace(/\/$/, '') + '/SHA256SUMS');
  let want = '';
  for (const line of sums.split('\n')) {
    const fields = line.trim().split(/\s+/);
    if (fields.length === 2 && (fields[1] === asset || fields[1] === '*' + asset)) {
      want = fields[0].toLowerCase();
      break;
    }
  }
  if (!want) {
    throw new Error('SHA256SUMS has no entry for ' + asset);
  }
  const payload = await fetchBuffer(base.replace(/\/$/, '') + '/' + asset);
  const got = crypto.createHash('sha256').update(payload).digest('hex').toLowerCase();
  if (got !== want) {
    throw new Error('SHA256 mismatch for ' + asset);
  }
  const targetDir = path.join(dataDir(home), 'bin');
  fs.mkdirSync(targetDir, { recursive: true });
  const target = path.join(targetDir, 'rolling-memory' + exe);
  const tmp = target + '.dl.' + process.pid;
  try {
    fs.writeFileSync(tmp, payload, { mode: 0o755 });
    fs.renameSync(tmp, target);
  } catch (err) {
    try { fs.unlinkSync(tmp); } catch (_) {}
    throw err;
  }
  return target;
}

async function main() {
  const home = process.env.HOME || process.env.USERPROFILE || os.homedir();
  for (const candidate of candidateCommands(home)) {
    try {
      if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) {
        const result = execCommand(candidate, ['mcp']);
        if (result.error) {
          // keep probing: a candidate that cannot be spawned (lost exec bit,
          // wrong architecture) must not stop the next one
          process.stderr.write('[rm-mcp] cannot run ' + candidate + ': ' + result.error.message + '\n');
          continue;
        }
        process.exit(typeof result.status === 'number' ? result.status : 127);
      }
    } catch (err) {
      // keep probing: an unreadable candidate must not stop the next one
    }
  }
  const explicitBase = process.env.ROLLING_MEMORY_RELEASE_BASE !== undefined && process.env.ROLLING_MEMORY_RELEASE_BASE !== '';
  const base = process.env.ROLLING_MEMORY_RELEASE_BASE === undefined
    ? DEFAULT_RELEASE_BASE
    : process.env.ROLLING_MEMORY_RELEASE_BASE;
  const mirror = process.env.ROLLING_MEMORY_RELEASE_MIRROR === undefined
    ? DEFAULT_RELEASE_MIRROR
    : process.env.ROLLING_MEMORY_RELEASE_MIRROR;
  const channels = [];
  if (base && !['off', 'none', '0'].includes(base)) {
    channels.push(base);
    if (!explicitBase && mirror && !['off', 'none', '0'].includes(mirror) && mirror !== base) {
      channels.push(mirror);
    }
  }
  for (const ch of channels) {
    try {
      process.stderr.write('[rm-mcp] first use: fetching the binary from the release channel (' + ch + ', SHA256 verified)…\n');
      const installed = await bootstrap(home, ch);
      const result = execCommand(installed, ['mcp']);
      process.exit(typeof result.status === 'number' ? result.status : 127);
    } catch (err) {
      process.stderr.write('[rm-mcp] bootstrap failed (' + ch + '): ' + err.message + '\n');
      if (explicitBase || /SHA256 mismatch/.test(err.message)) {
        break;
      }
    }
  }
  process.stderr.write('[rm-mcp] no Rolling-Memory binary found; run the one-line installer (install.sh) or reinstall the plugin payload\n');
  process.exit(127);
}

main();
