// Run with: node --test ui/frontend/test/format.test.mjs
import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const F = require('../dist/format.js');

test('formatMs', () => {
  assert.equal(F.formatMs(undefined), '—');
  assert.equal(F.formatMs(null), '—');
  assert.equal(F.formatMs(NaN), '—');
  assert.equal(F.formatMs(-1), '—');
  assert.equal(F.formatMs(0), '0 ms');
  assert.equal(F.formatMs(3.46), '3.5 ms');
  assert.equal(F.formatMs(9.96), '10 ms');
  assert.equal(F.formatMs(42.4), '42 ms');
  assert.equal(F.formatMs(1234.6), '1235 ms');
});

test('formatPercent', () => {
  assert.equal(F.formatPercent(undefined), '—');
  assert.equal(F.formatPercent(0), '0%');
  assert.equal(F.formatPercent(0.04), '0%');
  assert.equal(F.formatPercent(0.5), '0.5%');
  assert.equal(F.formatPercent(2), '2%');
  assert.equal(F.formatPercent(2.34), '2.3%');
  assert.equal(F.formatPercent(12.3), '12%');
  assert.equal(F.formatPercent(100), '100%');
});

test('qualityBadge', () => {
  assert.deepEqual(F.qualityBadge('good'), { label: 'Good', cls: 'good' });
  assert.deepEqual(F.qualityBadge('fair'), { label: 'Fair', cls: 'fair' });
  assert.deepEqual(F.qualityBadge('poor'), { label: 'Poor', cls: 'poor' });
  assert.deepEqual(F.qualityBadge('unknown'), { label: 'Unknown', cls: 'unknown' });
  assert.deepEqual(F.qualityBadge(''), { label: 'Unknown', cls: 'unknown' });
  assert.deepEqual(F.qualityBadge('GOOD'), { label: 'Good', cls: 'good' });
  assert.deepEqual(F.qualityBadge('weird'), { label: 'Unknown', cls: 'unknown' });
});

test('pathLabel', () => {
  assert.equal(F.pathLabel('direct'), 'Direct');
  assert.equal(F.pathLabel('member-relay'), 'Via a member');
  assert.equal(F.pathLabel('shared-relay'), 'Relay server');
  assert.equal(F.pathLabel('tcp-fallback'), 'Relay server over TCP');
  assert.equal(F.pathLabel('none'), 'Not connected');
  assert.equal(F.pathLabel(''), 'Not connected');
  assert.equal(F.pathLabel('something-new'), 'Not connected');
});

test('relayDetail', () => {
  assert.equal(F.relayDetail({ path: 'direct', relay_detail: '' }), '');
  assert.equal(F.relayDetail({ path: 'member-relay', relay_detail: 'Mehmet' }), 'via Mehmet');
  assert.equal(F.relayDetail({ path: 'shared-relay', relay_detail: 'r.example:3478' }), 'via r.example:3478');
  assert.equal(F.relayDetail({ path: 'tcp-fallback', relay_detail: 'r.example:443' }), 'via r.example:443 (higher latency)');
  assert.equal(F.relayDetail({ path: 'shared-relay', relay_detail: '' }), '');
});

test('relayModeLabel', () => {
  assert.equal(F.relayModeLabel('udp'), 'UDP');
  assert.equal(F.relayModeLabel('tcp'), 'TCP fallback (higher latency)');
  assert.equal(F.relayModeLabel('down'), 'Down');
  assert.equal(F.relayModeLabel('x'), 'Unknown');
});

test('dhtLabel', () => {
  assert.equal(F.dhtLabel('off'), 'Off');
  assert.equal(F.dhtLabel('standby'), 'Standby (starts only if no relay is reachable)');
  assert.equal(F.dhtLabel('running'), 'Running');
  assert.equal(F.dhtLabel(''), 'Unknown');
});

test('friendPresence', () => {
  const now = Date.parse('2026-01-10T12:00:00Z');
  assert.equal(F.friendPresence({ online: true }, now), 'Online now');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '0001-01-01T00:00:00Z' }, now), 'Never seen');
  assert.equal(F.friendPresence({ online: false }, now), 'Never seen');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '2026-01-10T11:59:40Z' }, now), 'Last seen just now');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '2026-01-10T11:55:00Z' }, now), 'Last seen 5 min ago');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '2026-01-10T09:00:00Z' }, now), 'Last seen 3 h ago');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '2026-01-07T12:00:00Z' }, now), 'Last seen 3 days ago');
  assert.equal(F.friendPresence({ online: false, last_seen_at: '2026-01-09T10:00:00Z' }, now), 'Last seen 1 day ago');
});

test('formatExpiry', () => {
  const now = Date.parse('2026-01-10T12:00:00Z');
  assert.equal(F.formatExpiry('2026-01-10T12:04:12Z', now), 'Expires in 4 min 12 s');
  assert.equal(F.formatExpiry('2026-01-10T12:00:30Z', now), 'Expires in 30 s');
  assert.equal(F.formatExpiry('2026-01-10T12:10:00Z', now), 'Expires in 10 min');
  assert.equal(F.formatExpiry('2026-01-10T11:59:00Z', now), 'Expired');
  assert.equal(F.formatExpiry('garbage', now), '');
});

test('relay formatting and validation', () => {
  assert.equal(F.formatRelay({ host: 'r.example', udp_port: 3478, tcp_port: 443 }), 'r.example (UDP 3478, TCP 443)');
  assert.equal(F.validatePort('3478'), '');
  assert.equal(F.validatePort('0'), 'Enter a port between 1 and 65535.');
  assert.equal(F.validatePort('70000'), 'Enter a port between 1 and 65535.');
  assert.equal(F.validatePort('abc'), 'Enter a port between 1 and 65535.');
  assert.equal(F.validatePort('1.5'), 'Enter a port between 1 and 65535.');
  assert.equal(F.validatePort('0', { allowZero: true }), '');
  assert.equal(F.validatePort('', { allowZero: true }), 'Enter a port between 0 and 65535.');
  assert.equal(F.validateRelay({ host: '', udp: '3478', tcp: '443' }), 'Enter the relay host name or IP address.');
  assert.equal(F.validateRelay({ host: 'has space', udp: '3478', tcp: '443' }), 'The host name cannot contain spaces.');
  assert.equal(F.validateRelay({ host: 'r.example', udp: 'x', tcp: '443' }), 'UDP port: Enter a port between 1 and 65535.');
  assert.equal(F.validateRelay({ host: 'r.example', udp: '3478', tcp: '0' }), 'TCP port: Enter a port between 1 and 65535.');
  assert.equal(F.validateRelay({ host: 'r.example', udp: '3478', tcp: '443' }), '');
  assert.deepEqual(F.relayFromForm({ host: ' r.example ', udp: '3478', tcp: '443' }), { host: 'r.example', udp_port: 3478, tcp_port: 443 });
});

test('isPeerMeasured: only online peers with a path have numbers', () => {
  assert.equal(F.isPeerMeasured({ online: true, path: 'direct' }), true);
  assert.equal(F.isPeerMeasured({ online: true, path: 'none' }), false);
  assert.equal(F.isPeerMeasured({ online: false, path: 'direct' }), false);
});

test('canAddMember lists only active friends who are not members', () => {
  const friends = [
    { public_key: 'a', nickname: 'A', state: 'active' },
    { public_key: 'b', nickname: 'B', state: 'pending' },
    { public_key: 'c', nickname: 'C', state: 'active' },
  ];
  const net = { members: [{ public_key: 'c' }] };
  assert.deepEqual(F.addableFriends(friends, net).map((f) => f.public_key), ['a']);
});

test('plural', () => {
  assert.equal(F.plural(1, 'member'), '1 member');
  assert.equal(F.plural(0, 'member'), '0 members');
  assert.equal(F.plural(3, 'member'), '3 members');
});

// ---- helpers of the compact UI ----

test('looksLikeInvite', () => {
  assert.equal(F.looksLikeInvite('lanfolk-i1' + 'y'.repeat(106)), true);
  assert.equal(F.looksLikeInvite('  lanfolk-i1' + 'y'.repeat(106) + '\n'), true);
  assert.equal(F.looksLikeInvite('lanfolk1abc'), false);
  assert.equal(F.looksLikeInvite('lanfolk-i1 has spaces in it and is long enough to pass'), false);
  assert.equal(F.looksLikeInvite(''), false);
  assert.equal(F.looksLikeInvite(null), false);
});

test('inviteValidity', () => {
  const now = Date.parse('2026-11-04T12:00:00Z');
  assert.equal(F.inviteValidity('2026-11-11T12:00:00Z', now), 'Works for 7 more days');
  assert.equal(F.inviteValidity('2026-11-05T13:00:00Z', now), 'Works for 1 more day');
  assert.equal(F.inviteValidity('2026-11-04T17:30:00Z', now), 'Works for 5 more hours');
  assert.equal(F.inviteValidity('2026-11-04T12:30:00Z', now), 'Works for less than an hour');
  assert.equal(F.inviteValidity('2026-11-04T11:00:00Z', now), 'Expired');
  assert.equal(F.inviteValidity('garbage', now), '');
});

test('memberSummary', () => {
  assert.deepEqual(F.memberSummary({ online: true, path: 'direct', ping_ms: 18.2 }), { text: '18 ms', sub: 'Direct', state: 'online' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'member-relay', relay_detail: 'Veli', ping_ms: 40 }), { text: '40 ms', sub: 'Via Veli', state: 'online' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'shared-relay', ping_ms: 151 }), { text: '151 ms', sub: 'Relay', state: 'online' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'tcp-fallback', ping_ms: 160 }), { text: '160 ms', sub: 'Relay (TCP)', state: 'online' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'none' }), { text: 'Connecting…', sub: '', state: 'connecting' });
  assert.deepEqual(F.memberSummary({ online: false, path: 'none' }), { text: 'Offline', sub: '', state: 'offline' });
  assert.deepEqual(F.memberSummary(undefined), { text: 'Offline', sub: '', state: 'offline' });
  // The row shows the steadier average when there is one, and says so when
  // the quality is not good.
  assert.deepEqual(F.memberSummary({ online: true, path: 'direct', ping_ms: 30, avg_ping_ms: 24.6, quality: 'good' }), { text: '25 ms', sub: 'Direct', state: 'online' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'direct', ping_ms: 90, avg_ping_ms: 85, quality: 'fair' }), { text: '85 ms', sub: 'Direct · Fair', state: 'fair' });
  assert.deepEqual(F.memberSummary({ online: true, path: 'shared-relay', ping_ms: 150, avg_ping_ms: 131.4, quality: 'poor' }), { text: '131 ms', sub: 'Relay · Poor', state: 'poor' });
});

test('extractInvite finds the code in a chat message', () => {
  const code = 'lanfolk-i1' + 'k'.repeat(40);
  assert.equal(F.extractInvite(code), code);
  assert.equal(F.extractInvite(F.inviteMessage(code, 'Friday games', 'https://example.org/dl')), code);
  assert.equal(F.extractInvite('Mehmet: here you go ' + code + ' have fun'), code);
  assert.equal(F.extractInvite('lanfolk-i1short'), '');
  assert.equal(F.extractInvite(null), '');
  assert.equal(F.looksLikeInvite(F.inviteMessage(code, 'x', 'https://e')), false);
});

test('inviteMessage names the network and the download page', () => {
  const m = F.inviteMessage('lanfolk-i1abc', 'Friday games', 'https://example.org/dl');
  assert.match(m, /"Friday games"/);
  assert.match(m, /https:\/\/example\.org\/dl/);
  assert.ok(m.endsWith('lanfolk-i1abc'));
});

test('compareVersions and versionRelation', () => {
  const order = ['', '0.2.0-alpha', '0.2.0-alpha.1', '0.2.0-beta.2', '0.2.0-beta.11', '0.2.0-rc.1', '0.2.0', 'v0.2.1', '0.10.0', '1.0.0'];
  for (let i = 0; i < order.length; i++) {
    for (let j = 0; j < order.length; j++) {
      assert.equal(F.compareVersions(order[i], order[j]), Math.sign(i - j), order[i] + ' vs ' + order[j]);
    }
  }
  assert.equal(F.versionRelation('', '0.3.0'), 'older');
  assert.equal(F.versionRelation('0.2.9', '0.3.0'), 'older');
  assert.equal(F.versionRelation('0.3.0', '0.3.0'), '');
  assert.equal(F.versionRelation('0.3.1', '0.3.0'), 'newer');
  assert.equal(F.versionRelation('0.3.1', '0.0.0-dev'), '');
  assert.equal(F.versionRelation('0.3.1', ''), '');
});

test('connectionAdvice', () => {
  assert.equal(F.connectionAdvice({ online: true, path: 'direct', quality: 'good' }), '');
  assert.equal(F.connectionAdvice({ online: false, path: 'direct', quality: 'poor' }), '');
  assert.match(F.connectionAdvice({ online: true, path: 'shared-relay', quality: 'good' }), /relay/);
  assert.match(F.connectionAdvice({ online: true, path: 'direct', quality: 'poor', loss_pct: 5 }), /lost/);
  assert.match(F.connectionAdvice({ online: true, path: 'direct', quality: 'fair', jitter_ms: 40 }), /jumps/);
  assert.match(F.connectionAdvice({ online: true, path: 'shared-relay', quality: 'poor', loss_pct: 0, jitter_ms: 3 }), /port mapping/);
  assert.match(F.connectionAdvice({ online: true, path: 'direct', quality: 'poor', loss_pct: 0, jitter_ms: 3 }), /distance/);
});

test('joinStateText', () => {
  assert.equal(F.joinStateText({ state: 'waiting', owner_nickname: 'Zeynep' }), 'Waiting for Zeynep to come online…');
  assert.equal(F.joinStateText({ state: 'waiting' }), 'Waiting for the owner to come online…');
  assert.equal(F.joinStateText({ state: 'joining' }), 'Joining…');
  assert.equal(F.joinStateText({ state: 'joined' }), 'Joined');
  assert.equal(F.joinStateText({ state: 'expired' }), 'The invite expired. Ask for a new one.');
});

test('onlineCount counts you when you are connected', () => {
  const net = { members: [{ public_key: 'me', is_self: true }, { public_key: 'a' }, { public_key: 'b' }] };
  const peers = [{ public_key: 'a', online: true }, { public_key: 'b', online: false }];
  assert.deepEqual(F.onlineCount(net, peers, true), { online: 2, total: 3, others: 2 });
  assert.deepEqual(F.onlineCount(net, peers, false), { online: 1, total: 3, others: 2 });
  assert.deepEqual(F.onlineCount(net, [], true), { online: 1, total: 3, others: 2 });
  assert.deepEqual(F.onlineCount({ members: [{ is_self: true }] }, [], true), { online: 1, total: 1, others: 0 });
});

test('defaultNetworkName', () => {
  assert.equal(F.defaultNetworkName('Ahmet'), "Ahmet's network");
  assert.equal(F.defaultNetworkName(''), 'My network');
  assert.ok(F.defaultNetworkName('x'.repeat(60)).length <= 32);
});

test('inviteValidity rounds a fresh code to whole days', () => {
  const now = Date.parse('2026-11-04T12:00:00Z');
  assert.equal(F.inviteValidity('2026-11-11T11:59:00Z', now), 'Works for 7 more days');
  assert.equal(F.inviteValidity('2026-11-05T10:00:00Z', now), 'Works for 22 more hours');
});
