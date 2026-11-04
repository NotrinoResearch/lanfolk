#!/usr/bin/env node
// Client for the UI automation API of a development build of Lanfolk.exe
// (built with -tags automation, started with -automation 127.0.0.1:9400).
// It reads the address and token from %TEMP%\lanfolk-automation.json.
//
//   node scripts/dev/ui.mjs eval "return document.title"
//   node scripts/dev/ui.mjs click '[data-action="go"][data-view="join"]'
//   node scripts/dev/ui.mjs type '#jn-code' 'lanfolk-i1...'
//   node scripts/dev/ui.mjs text            # visible text of the view
//   node scripts/dev/ui.mjs shot out.png
//   node scripts/dev/ui.mjs window show|hide|minimise|move X Y
//   node scripts/dev/ui.mjs tray            # tray model as JSON
//   node scripts/dev/ui.mjs tray open|copy-ip|settings|invite|disconnect|toggle|network ID
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const info = JSON.parse(fs.readFileSync(path.join(os.tmpdir(), 'lanfolk-automation.json'), 'utf8'));
const base = `http://${info.addr}`;
const auth = { Authorization: `Bearer ${info.token}` };

async function req(method, p, body) {
  const r = await fetch(base + p, { method, headers: auth, body });
  if (!r.ok) throw new Error(`${r.status} ${await r.text()}`);
  return r;
}
const evalJS = async (js) => (await req('POST', '/eval', js)).json();

const [cmd, ...args] = process.argv.slice(2);
let out;
switch (cmd) {
  case 'eval':
    out = await evalJS(args.join(' '));
    break;
  case 'click':
    out = await evalJS(`const e = document.querySelector(${JSON.stringify(args[0])}); if (!e) throw new Error('not found: ' + ${JSON.stringify(args[0])}); e.click(); await new Promise(r => setTimeout(r, 150)); return true;`);
    break;
  case 'type':
    out = await evalJS(`const e = document.querySelector(${JSON.stringify(args[0])}); if (!e) throw new Error('not found'); e.focus(); e.value = ${JSON.stringify(args.slice(1).join(' '))}; e.dispatchEvent(new Event('input', { bubbles: true })); e.dispatchEvent(new Event('change', { bubbles: true })); return true;`);
    break;
  case 'text':
    out = await evalJS(`return document.querySelector('#view').innerText`);
    break;
  case 'shot': {
    const r = await req('GET', '/screenshot');
    fs.writeFileSync(args[0] || 'lanfolk.png', Buffer.from(await r.arrayBuffer()));
    out = args[0] || 'lanfolk.png';
    break;
  }
  case 'window': {
    const q = new URLSearchParams({ op: args[0] });
    if (args[0] === 'move') { q.set('x', args[1]); q.set('y', args[2]); }
    out = await (await req('POST', '/window?' + q)).text();
    break;
  }
  case 'tray':
    if (!args[0]) out = await (await req('GET', '/tray')).json();
    else out = await (await req('POST', '/tray?' + new URLSearchParams({ item: args[0], id: args[1] || '' }))).text();
    break;
  default:
    console.error('commands: eval, click, type, text, shot, window, tray');
    process.exit(2);
}
console.log(typeof out === 'string' ? out : JSON.stringify(out, null, 2));
