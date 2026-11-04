// Generates every Lanfolk brand asset from one geometric definition.
//
//   cd scripts/brand && npm install && node build.mjs
//
// Needs Node 22+ and Microsoft Edge (used headless to rasterize the SVGs).
// Outputs: assets/brand/*.svg|png|ico, ui/assets/*.ico (app and tray icons),
// ui/frontend/dist/brand/*.svg and installer/assets/*.png.
//
// The mark: four nodes on a diamond joined by three links. The left node,
// in green, is you; the missing link is the open seat an invite fills.
// Links stop short of the nodes. Below 24 px an optical variant with larger
// nodes and a shorter gap keeps the links visible.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import opentype from 'opentype.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const out = (...p) => {
  const f = path.join(root, ...p);
  fs.mkdirSync(path.dirname(f), { recursive: true });
  return f;
};

export const color = {
  cobalt: '#2F5BEA', cobaltDark: '#6B8BFF', signal: '#14C38E', signalDark: '#3DDC97',
  mint: '#7CF2C4', ink: '#0F172A', paper: '#FFFFFF', gray: '#8A8A8A', amber: '#E3A008', red: '#E5484D',
};

const nodes = [[24, 8.5], [39.5, 24], [24, 39.5], [8.5, 24]]; // top, right, bottom, left (you)
const links = [[0, 1], [1, 2], [2, 3]];
const regular = { r: 6, w: 4.5, gap: 3.2 };
const small = { r: 6.9, w: 5.4, gap: 2.1 };

const f = (n) => +n.toFixed(3);

// glyph returns SVG elements (no <svg> wrapper) for a 48 x 48 box.
function glyph({ a, you, geo = regular, youStyle = 'fill', scale = 1, dx = 0, dy = 0 }) {
  const t = (x, y) => [dx + x * scale, dy + y * scale];
  let s = '';
  for (const [i, j] of links) {
    const [x1, y1] = nodes[i];
    const [x2, y2] = nodes[j];
    const L = Math.hypot(x2 - x1, y2 - y1);
    const o = geo.r + geo.gap;
    const ux = (x2 - x1) / L;
    const uy = (y2 - y1) / L;
    const p1 = t(x1 + ux * o, y1 + uy * o);
    const p2 = t(x2 - ux * o, y2 - uy * o);
    s += `<line x1="${f(p1[0])}" y1="${f(p1[1])}" x2="${f(p2[0])}" y2="${f(p2[1])}" stroke="${a}" stroke-width="${f(geo.w * scale)}" stroke-linecap="round"/>`;
  }
  nodes.forEach(([x, y], i) => {
    const [cx, cy] = t(x, y);
    if (i === 3 && youStyle === 'ring') {
      const sw = 2.8 * scale;
      s += `<circle cx="${f(cx)}" cy="${f(cy)}" r="${f(geo.r * scale - sw / 2)}" fill="none" stroke="${you}" stroke-width="${f(sw)}"/>`;
    } else {
      s += `<circle cx="${f(cx)}" cy="${f(cy)}" r="${f(geo.r * scale)}" fill="${i === 3 ? you : a}"/>`;
    }
  });
  return s;
}

const svg = (w, h, body, title) =>
  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}"` +
  (title ? ` role="img" aria-label="${title}"><title>${title}</title>` : '>') + body + '</svg>\n';

// appIcon is a rounded tile with white nodes and a mint "you" node.
function appIcon(geo = regular) {
  // Small sizes give the glyph more of the tile so the links survive.
  const sc = geo === small ? 0.8 : 0.68;
  const off = (48 - 48 * sc) / 2;
  return `<rect width="48" height="48" rx="10.5" fill="${color.cobalt}"/>` +
    glyph({ a: color.paper, you: color.mint, geo, scale: sc, dx: off, dy: off });
}

// Tray glyphs fill their box and are colored by state; they work on light
// and dark taskbars.
const tray = {
  online: { a: '#4D74FF', you: '#22C993' },
  offline: { a: color.gray, you: color.gray },
  connecting: { a: '#4D74FF', you: color.amber, youStyle: 'ring' },
  attention: { a: color.gray, you: color.red },
};

// ---- wordmark: Geist SemiBold (SIL OFL), converted to outlines ----
const font = opentype.loadSync(path.join(here, 'node_modules/geist/dist/fonts/geist-sans/Geist-SemiBold.ttf'));
function wordmark(text, size, x, baseline, fill) {
  const tracking = -0.02 * size;
  let pen = x;
  let d = '';
  for (const ch of text) {
    const g = font.charToGlyph(ch);
    d += g.getPath(pen, baseline, size).toPathData(2);
    pen += (g.advanceWidth / font.unitsPerEm) * size + tracking;
  }
  return { d: `<path d="${d}" fill="${fill}"/>`, width: pen - x - tracking };
}
function logo(dark) {
  const size = 40;
  const capH = (font.tables.os2.sCapHeight / font.unitsPerEm) * size;
  const markSize = 58;
  const gapX = 10;
  const h = 58;
  const baseline = (h + capH) / 2;
  const wm = wordmark('Lanfolk', size, markSize + gapX, baseline, dark ? '#FFFFFF' : color.ink);
  const body = glyph({
    a: dark ? color.cobaltDark : color.cobalt, you: dark ? color.signalDark : color.signal,
    scale: markSize / 48, dy: (h - markSize) / 2,
  }) + wm.d;
  return svg(Math.ceil(markSize + gapX + wm.width + 2), h, body, 'Lanfolk');
}

// ---- vector files ----
const files = {
  'assets/brand/mark.svg': svg(48, 48, glyph({ a: color.cobalt, you: color.signal }), 'Lanfolk'),
  'assets/brand/mark-small.svg': svg(48, 48, glyph({ a: color.cobalt, you: color.signal, geo: small }), 'Lanfolk'),
  'assets/brand/mark-on-dark.svg': svg(48, 48, glyph({ a: color.cobaltDark, you: color.signalDark }), 'Lanfolk'),
  'assets/brand/mark-mono.svg': svg(48, 48, glyph({ a: 'currentColor', you: 'currentColor' }), 'Lanfolk'),
  'assets/brand/app-icon.svg': svg(48, 48, appIcon(), 'Lanfolk'),
  'assets/brand/logo.svg': logo(false),
  'assets/brand/logo-on-dark.svg': logo(true),
};
for (const [k, v] of Object.entries(tray)) files[`assets/brand/tray-${k}.svg`] = svg(48, 48, glyph({ ...v, geo: small }));
for (const [p, s] of Object.entries(files)) fs.writeFileSync(out(p), s);
for (const n of ['mark.svg', 'mark-on-dark.svg', 'logo.svg', 'logo-on-dark.svg', 'app-icon.svg']) {
  fs.copyFileSync(out('assets/brand', n), out('ui/frontend/dist/brand', n));
}

// ---- raster files ----
const jobs = [];
const appSvg = (sz) => svg(48, 48, appIcon(sz < 24 ? small : regular));
for (const sz of [16, 20, 24, 32, 40, 48, 64, 96, 128, 256]) jobs.push({ svg: appSvg(sz), size: sz, ico: 'app' });
for (const sz of [256, 512, 1024]) jobs.push({ svg: appSvg(sz), size: sz, file: `assets/brand/app-icon-${sz}.png` });
for (const [k, v] of Object.entries(tray)) {
  // Every tray frame uses the small geometry: the shell always shows tray
  // icons at small-icon size, and some tray code loads the 32 px frame and
  // scales it down.
  for (const sz of [16, 20, 24, 32, 40, 48]) {
    jobs.push({ svg: svg(48, 48, glyph({ ...v, geo: small })), size: sz, ico: `tray-${k}` });
  }
}
jobs.push({ svg: svg(48, 48, glyph({ a: color.cobalt, you: color.signal })), size: 110, file: 'installer/assets/wizard-small.png' });
jobs.push({ svg: svg(48, 48, glyph({ a: color.cobalt, you: color.signal })), size: 220, file: 'installer/assets/wizard-small@2x.png' });
// Welcome and finish page image: cobalt field, the mark large and bleeding
// off the right edge, the wordmark at the bottom.
function wizardLarge() {
  // 202 x 386 is what Inno Setup 6.7 recommends for WizardSizePercent=110.
  const w = 202;
  const h = 386;
  const sc = 5.4;
  const mark = glyph({ a: 'rgba(255,255,255,0.16)', you: color.mint, scale: sc, dx: 138 - 24 * sc, dy: 158 - 24 * sc });
  const wm = wordmark('Lanfolk', 26, 22, h - 32, '#FFFFFF');
  const small = glyph({ a: '#FFFFFF', you: color.mint, scale: 0.72, dx: 19, dy: h - 100 });
  return svg(w, h, `<rect width="${w}" height="${h}" fill="${color.cobalt}"/>${mark}${small}${wm.d}`);
}
jobs.push({ svg: wizardLarge(), size: 0, w: 202, file: 'installer/assets/wizard-large.png' });
jobs.push({ svg: wizardLarge(), size: 0, w: 404, file: 'installer/assets/wizard-large@2x.png' });
jobs.push({ svg: logo(false), size: 0, w: 600, file: 'assets/brand/logo.png' });
jobs.push({ svg: logo(true), size: 0, w: 600, file: 'assets/brand/logo-on-dark.png' });

const pngs = await rasterize(jobs);
const icoSets = {};
jobs.forEach((j, i) => {
  if (j.file) fs.writeFileSync(out(j.file), pngs[i]);
  if (j.ico) (icoSets[j.ico] ||= []).push({ size: j.size, png: pngs[i] });
});
for (const [name, imgs] of Object.entries(icoSets)) {
  const targets = name === 'app' ? ['ui/assets/lanfolk.ico', 'assets/brand/lanfolk.ico'] : [`ui/assets/${name}.ico`];
  for (const t of targets) fs.writeFileSync(out(t), ico(imgs));
}
console.log(`brand assets written (${jobs.length} rasters)`);

// ico packs PNG images into an .ico container (PNG entries need Vista+).
function ico(imgs) {
  const head = Buffer.alloc(6 + 16 * imgs.length);
  head.writeUInt16LE(0, 0);
  head.writeUInt16LE(1, 2);
  head.writeUInt16LE(imgs.length, 4);
  let offset = head.length;
  imgs.forEach((im, i) => {
    const e = 6 + 16 * i;
    const dim = im.size >= 256 ? 0 : im.size;
    head.writeUInt8(dim, e);
    head.writeUInt8(dim, e + 1);
    head.writeUInt16LE(1, e + 4);
    head.writeUInt16LE(32, e + 6);
    head.writeUInt32LE(im.png.length, e + 8);
    head.writeUInt32LE(offset, e + 12);
    offset += im.png.length;
  });
  return Buffer.concat([head, ...imgs.map((im) => im.png)]);
}

// rasterize draws each SVG onto a canvas in headless Edge and returns PNGs.
async function rasterize(list) {
  const port = 9500 + Math.floor(Math.random() * 300);
  const exe = process.env.EDGE || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe';
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'lanfolk-brand-'));
  const edge = spawn(exe, ['--headless=new', `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`, '--no-first-run', 'about:blank'], { stdio: 'ignore' });
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  let page;
  for (let i = 0; i < 100 && !page; i++) {
    try {
      const targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
      page = targets.find((t) => t.type === 'page');
    } catch { /* not up yet */ }
    if (!page) await sleep(150);
  }
  if (!page) throw new Error('headless Edge did not start');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((r) => { ws.onopen = r; });
  let id = 0;
  const pending = new Map();
  ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
  };
  const send = (method, params = {}) => new Promise((r) => {
    const i = ++id;
    pending.set(i, r);
    ws.send(JSON.stringify({ id: i, method, params }));
  });
  const res = [];
  for (const j of list) {
    const expr = `(async () => {
      const img = new Image();
      img.src = 'data:image/svg+xml;base64,' + btoa(unescape(encodeURIComponent(${JSON.stringify(j.svg)})));
      await img.decode();
      let w = ${j.size}, h = ${j.size};
      if (!w) { w = ${j.w || 0}; h = Math.round(w * img.naturalHeight / img.naturalWidth); }
      const c = document.createElement('canvas'); c.width = w; c.height = h;
      c.getContext('2d').drawImage(img, 0, 0, w, h);
      return c.toDataURL('image/png').split(',')[1];
    })()`;
    const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
    res.push(Buffer.from(r.result.result.value, 'base64'));
  }
  ws.close();
  // Edge starts several child processes; end the whole tree.
  spawnSync('taskkill', ['/PID', String(edge.pid), '/T', '/F'], { stdio: 'ignore' });
  try {
    fs.rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 300 });
  } catch { /* a file may still be locked for a moment; the temp folder is harmless */ }
  return res;
}
