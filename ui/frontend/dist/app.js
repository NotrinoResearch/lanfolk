// Lanfolk desktop UI. Plain JavaScript, no build step.
//
// The Go side (ui/app.go) is bound as window.go.main.App and forwards every
// call to the Lanfolk service. State arrives as events: "service", "status",
// "networks", "peers", "friends", "invites", plus "navigate" from the tray.
//
// Rendering: each view is a function that returns an HTML string; render()
// morphs the live DOM toward it, so focus, scroll and typed text survive the
// once-a-second peer updates. Clicks are handled by delegation on
// data-action attributes.
(function () {
  'use strict';

  var F = window.LanfolkFormat;
  var api = (window.go && window.go.main && window.go.main.App) || {};
  var rt = window.runtime || {};

  var store = {
    get: function (k) { try { return window.localStorage.getItem('lanfolk.' + k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem('lanfolk.' + k, v); } catch (e) { /* storage off */ } },
  };

  var S = {
    service: null, // {connected, message} once known
    status: null,
    networks: [],
    peers: [],
    friends: [],
    invites: [],
    settings: null,
    autostart: null,
    view: 'home', // home | create | join | invite | settings
    args: {},
    open: {}, // network id -> expanded
    detail: null, // public key of the member whose details are open
    busy: {},
    inviteCode: null, // {network_id, code, expires}
    copied: null,
    joinError: '',
  };

  // ---------------------------------------------------------------- utils --

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }
  function attr(name, val) { return val ? ' ' + name : ''; }
  function errText(e) {
    if (!e) return 'Something went wrong.';
    if (typeof e === 'string') return e;
    return e.message || String(e);
  }
  function call(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    if (typeof api[name] !== 'function') return Promise.reject('The app is still starting. Try again in a moment.');
    return api[name].apply(api, args);
  }
  function $(sel) { return document.querySelector(sel); }

  var toastTimer;
  function toast(msg, kind) {
    var box = $('#toasts');
    box.innerHTML = '<div class="toast' + (kind === 'bad' ? ' bad' : '') + '">' +
      (kind === 'ok' ? icon('check') : '') + '<span>' + esc(msg) + '</span></div>';
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { box.innerHTML = ''; }, kind === 'bad' ? 6000 : 2400);
  }
  function fail(e) { toast(errText(e), 'bad'); }

  function copy(text, label) {
    return call('CopyToClipboard', text).then(function () { toast((label || 'Copied') + '', 'ok'); }, fail);
  }

  // ---------------------------------------------------------------- morph --

  function morph(target, html) {
    var tpl = document.createElement('template');
    tpl.innerHTML = html;
    morphChildren(target, tpl.content);
  }
  function sameNode(a, b) {
    if (a.nodeType !== b.nodeType) return false;
    if (a.nodeType !== 1) return true;
    return a.tagName === b.tagName && a.getAttribute('data-key') === b.getAttribute('data-key');
  }
  function morphChildren(parent, next) {
    var a = parent.firstChild;
    var b = next.firstChild;
    while (b) {
      var nb = b.nextSibling;
      if (!a) {
        parent.appendChild(b);
      } else if (sameNode(a, b)) {
        morphNode(a, b);
        a = a.nextSibling;
      } else {
        var na = a.nextSibling;
        parent.replaceChild(b, a);
        a = na;
      }
      b = nb;
    }
    while (a) {
      var n = a.nextSibling;
      parent.removeChild(a);
      a = n;
    }
  }
  function morphNode(a, b) {
    if (a.nodeType === 3 || a.nodeType === 8) {
      if (a.nodeValue !== b.nodeValue) a.nodeValue = b.nodeValue;
      return;
    }
    var i, n;
    for (i = a.attributes.length - 1; i >= 0; i--) {
      n = a.attributes[i].name;
      if (!b.hasAttribute(n)) a.removeAttribute(n);
    }
    for (i = 0; i < b.attributes.length; i++) {
      n = b.attributes[i];
      if (a.getAttribute(n.name) !== n.value) a.setAttribute(n.name, n.value);
    }
    if (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA') {
      if (a.type === 'checkbox') a.checked = b.hasAttribute('checked');
      else if (document.activeElement !== a && b.hasAttribute('value') && a.value !== b.getAttribute('value')) a.value = b.getAttribute('value');
      if (a.tagName === 'TEXTAREA' && document.activeElement !== a && a.value !== b.textContent && b.hasAttribute('data-sync')) a.value = b.textContent;
      return;
    }
    morphChildren(a, b);
  }

  var queued = false;
  function render() {
    if (queued) return;
    queued = true;
    requestAnimationFrame(function () {
      queued = false;
      var view = $('#view');
      morph(view, viewHTML());
      $('#tb-settings').setAttribute('aria-current', S.view === 'settings' ? 'page' : 'false');
      $('#tb-settings').hidden = !S.service || !S.service.connected || isOnboarding();
      var auto = view.querySelector('[data-autofocus]');
      if (auto && !view.contains(document.activeElement)) {
        auto.removeAttribute('data-autofocus');
        auto.focus();
        if (auto.select) auto.select();
      }
    });
  }

  // ---------------------------------------------------------------- model --

  function activeNetwork() {
    for (var i = 0; i < S.networks.length; i++) if (S.networks[i].active) return S.networks[i];
    return null;
  }
  function networkById(id) {
    for (var i = 0; i < S.networks.length; i++) if (S.networks[i].id === id) return S.networks[i];
    return null;
  }
  function peerByKey(k) {
    for (var i = 0; i < S.peers.length; i++) if (S.peers[i].public_key === k) return S.peers[i];
    return null;
  }
  function openJoins() {
    return S.invites.filter(function (j) {
      if (j.state === 'joined') return false;
      return !(j.state === 'expired' && networkById(j.network_id));
    });
  }
  function isOnboarding() {
    return !!(S.service && S.service.connected && S.status && !store.get('onboarded') &&
      S.networks.length === 0 && openJoins().length === 0);
  }
  function selfState() {
    var net = activeNetwork();
    if (!net) return { dot: 'offline', text: 'Offline' };
    if (S.status && S.status.adapter_error) return { dot: 'attention', text: 'Network adapter problem' };
    if (S.status && !S.status.adapter_up) return { dot: 'connecting', text: 'Connecting to ' + net.name + '…' };
    return { dot: 'online', text: 'Online in ' + net.name };
  }

  // ---------------------------------------------------------------- views --

  function viewHTML() {
    if (!S.service) return '<div class="empty" data-key="loading"><span class="spinner"></span></div>';
    if (!S.service.connected) return offlineHTML();
    if (!S.status) return '<div class="empty" data-key="loading"><span class="spinner"></span></div>';
    if (isOnboarding() && S.view === 'home') return onboardingHTML();
    switch (S.view) {
      case 'create': return createHTML();
      case 'join': return joinHTML();
      case 'invite': return inviteHTML();
      case 'settings': return settingsHTML();
      default: return homeHTML();
    }
  }

  function offlineHTML() {
    return '<div class="empty mute" data-key="offline">' + mark(56) +
      '<h2>The Lanfolk service is not running</h2>' +
      '<p>Lanfolk needs its background service to reach your friends. If you just installed it, restart your computer.' +
      (S.service.message ? ' ' + esc(S.service.message) : '') + '</p>' +
      '<div class="stack"><button type="button" class="btn" data-action="retry">' + icon('refresh') + 'Try again</button></div>' +
      '<p class="hint" style="margin-top:14px">This window reconnects by itself.</p></div>';
  }

  function onboardingHTML() {
    var nick = S.status.nickname || '';
    return '<div class="onboard" data-key="onboard"><div class="onboard-top">' + mark(48) +
      '<h1>Welcome to Lanfolk</h1>' +
      '<p class="lede">Your friends on one local network, wherever they are. LAN games just work.</p>' +
      '<form data-form="onboard" autocomplete="off">' +
      '<div class="field"><label for="ob-name">What should your friends call you?</label>' +
      '<input class="input" id="ob-name" name="nick" maxlength="32" value="' + esc(nick) + '" data-autofocus spellcheck="false"></div>' +
      '<button type="submit" class="btn primary block">Continue</button></form>' +
      '</div><p class="note">' + icon('external') + '<span>Lanfolk keeps running in the notification area when you close this window.</span></p></div>';
  }

  function homeHTML() {
    var me = selfState();
    var net = activeNetwork();
    var ip = S.status.virtual_ip;
    var html = '<section class="me" data-key="me">' +
      '<button type="button" class="power' + (net ? ' on' : '') + '" data-action="power" aria-pressed="' + !!net + '"' +
      attr('disabled', S.networks.length === 0) + ' title="' + (net ? 'Go offline' : 'Go online') + '" aria-label="' + (net ? 'Go offline' : 'Go online') + '">' + icon('power') + '</button>' +
      '<div class="me-text"><div class="me-name">' + esc(S.status.nickname) + '</div>' +
      '<div class="me-status"><span class="dot ' + me.dot + '"></span><span>' + esc(me.text) + '</span></div></div>' +
      (ip ? '<button type="button" class="ip-chip" data-action="copy" data-text="' + esc(ip) + '" data-label="IP address copied" title="Copy your IP address">' + esc(ip) + icon('copy') + '</button>' : '') +
      '</section>';
    html += bannersHTML();
    var joins = openJoins();
    if (S.networks.length === 0 && joins.length === 0) {
      return html + '<div class="empty" data-key="empty">' + mark(44) +
        '<h2>No networks yet</h2><p>Create a network and send your friends the invite, or join with an invite someone sent you.</p>' +
        '<div class="stack"><button type="button" class="btn primary" data-action="go" data-view="create">' + icon('plus') + 'Create a network</button>' +
        '<button type="button" class="btn" data-action="go" data-view="join">' + icon('join') + 'Join with an invite</button></div></div>';
    }
    html += '<div class="scroll" data-key="list"><div class="list">';
    joins.forEach(function (j) { html += pendingHTML(j); });
    sortedNetworks().forEach(function (n) { html += networkHTML(n); });
    html += '</div></div>';
    html += '<footer class="actions" data-key="actions">' +
      '<button type="button" class="btn" data-action="go" data-view="create">' + icon('plus') + 'Create</button>' +
      '<button type="button" class="btn primary" data-action="go" data-view="join">' + icon('join') + 'Join network</button></footer>';
    return html;
  }

  function bannersHTML() {
    var st = S.status;
    if (st.adapter_error) {
      return '<div class="banner bad" data-key="b-adapter" role="alert">' + icon('warning') +
        '<div><strong>The network adapter could not start.</strong> ' + esc(st.adapter_error) +
        ' Restarting the computer usually helps.</div></div>';
    }
    var net = activeNetwork();
    var up = (st.relays || []).some(function (r) { return r.mode === 'udp' || r.mode === 'tcp'; });
    if (net && (st.relays || []).length && !up) {
      return '<div class="banner" data-key="b-relay">' + icon('warning') +
        '<div>No relay can be reached. Friends with a direct path still connect; others may not until it is back.</div></div>';
    }
    return '';
  }

  function sortedNetworks() {
    return S.networks.slice().sort(function (a, b) {
      if (a.active !== b.active) return a.active ? -1 : 1;
      return String(a.name).localeCompare(String(b.name), undefined, { sensitivity: 'base' });
    });
  }

  function pendingHTML(j) {
    var name = j.network_name || (j.owner_nickname ? j.owner_nickname + '’s network' : 'A network');
    var spinning = j.state === 'waiting' || j.state === 'joining';
    return '<div class="pending" data-key="join-' + esc(j.network_id) + '">' +
      (spinning ? '<span class="spinner" aria-hidden="true"></span>' : icon('warning')) +
      '<div class="pending-text"><div class="pending-title">' + esc(name) + '</div>' +
      '<div class="pending-sub">' + esc(F.joinStateText(j)) + '</div></div>' +
      '<button type="button" class="btn" style="height:28px;padding:0 10px;font-size:13px" data-action="cancel-join" data-id="' + esc(j.network_id) + '">' +
      (j.state === 'expired' ? 'Dismiss' : 'Cancel') + '</button></div>';
  }

  function networkHTML(n) {
    var open = S.open[n.id] != null ? S.open[n.id] : n.active;
    var count = F.onlineCount(n, n.active ? S.peers : []);
    var meta;
    if (n.active) meta = count.others === 0 ? 'Only you so far' : count.online + ' of ' + count.others + ' online';
    else meta = F.plural(count.others + 1, 'member');
    var html = '<div class="net' + (open ? ' open' : '') + (n.active ? ' active' : '') + '" data-key="net-' + esc(n.id) + '">' +
      '<div class="net-head">' +
      '<button type="button" class="net-toggle" data-action="toggle-net" data-id="' + esc(n.id) + '" aria-expanded="' + !!open + '">' +
      icon('chevron') + '<span class="net-name">' + esc(n.name) + '</span>' +
      (n.active ? '<span class="net-meta">' + esc(meta) + '</span>' : '<span class="net-meta">' + esc(meta) + '</span>') + '</button>' +
      (n.active ? '' : '<button type="button" class="btn net-connect" data-action="connect" data-id="' + esc(n.id) + '">Connect</button>') +
      '<button type="button" class="icon-btn" data-action="net-menu" data-id="' + esc(n.id) + '" aria-haspopup="menu" title="More" aria-label="More options for ' + esc(n.name) + '">' + icon('more') + '</button>' +
      '</div>';
    if (open) {
      html += '<ul class="members">';
      membersOf(n).forEach(function (m) { html += memberHTML(n, m); });
      if (n.is_owner && n.members.length <= 1) {
        html += '<li class="member" data-key="invite-hint"><button type="button" class="member-row" data-action="invite" data-id="' + esc(n.id) + '">' +
          '<span></span><span class="m-name" style="color:var(--accent-text)">Invite friends…</span><span></span></button></li>';
      }
      html += '</ul>';
    }
    return html + '</div>';
  }

  function membersOf(n) {
    var self = [];
    var rest = [];
    (n.members || []).forEach(function (m) { (m.is_self ? self : rest).push(m); });
    rest.sort(function (a, b) {
      var pa = n.active && peerByKey(a.public_key);
      var pb = n.active && peerByKey(b.public_key);
      var oa = !!(pa && pa.online);
      var ob = !!(pb && pb.online);
      if (oa !== ob) return oa ? -1 : 1;
      return String(a.nickname).localeCompare(String(b.nickname), undefined, { sensitivity: 'base' });
    });
    return self.concat(rest);
  }

  function memberHTML(n, m) {
    var key = m.public_key;
    var open = S.detail === n.id + '/' + key;
    var dot = 'offline';
    var right = '';
    if (m.is_self) {
      dot = n.active ? selfState().dot : 'offline';
      right = '';
    } else if (n.active) {
      var sum = F.memberSummary(peerByKey(key));
      dot = sum.state;
      right = sum.state === 'online'
        ? '<span class="m-right">' + esc(sum.text) + '<small>' + esc(sum.sub) + '</small></span>'
        : '<span class="m-right ' + sum.state + '">' + esc(sum.text) + '</span>';
    }
    var html = '<li class="member' + (open ? ' open' : '') + '" data-key="m-' + esc(key) + '">' +
      '<button type="button" class="member-row" data-action="member" data-id="' + esc(n.id) + '" data-key2="' + esc(key) + '" aria-expanded="' + open + '">' +
      '<span class="dot ' + dot + '" aria-hidden="true"></span>' +
      '<span class="m-name">' + esc(m.nickname || 'Unnamed') + (m.is_self ? '<small>you</small>' : '') + (m.is_owner ? '<small>owner</small>' : '') +
      '<span class="m-ip">' + esc(m.virtual_ip) + '</span></span>' + right + '</button>';
    if (open) html += detailHTML(n, m);
    return html + '</li>';
  }

  function detailHTML(n, m) {
    var p = n.active && !m.is_self ? peerByKey(m.public_key) : null;
    var rows = [['Address', m.virtual_ip]];
    if (p && F.isPeerMeasured(p)) {
      var q = F.qualityBadge(p.quality);
      var via = F.relayDetail(p);
      rows.push(['Connection', F.pathLabel(p.path) + (via ? ' · ' + via : '')]);
      rows.push(['Ping', F.formatMs(p.ping_ms) + ' (avg ' + F.formatMs(p.avg_ping_ms) + ')']);
      rows.push(['Jitter', F.formatMs(p.jitter_ms)]);
      rows.push(['Packet loss', F.formatPercent(p.loss_pct)]);
      rows.push(['Quality', '<span class="q-' + q.cls + '">' + esc(q.label) + '</span>', true]);
    } else if (!m.is_self && n.active) {
      rows.push(['Connection', p && p.online ? 'Connecting…' : 'Offline']);
    } else if (m.is_self) {
      rows.push(['This is you', n.active ? 'Connected' : 'Not connected']);
    }
    var html = '<div class="detail"><dl class="kv">';
    rows.forEach(function (r) { html += '<dt>' + esc(r[0]) + '</dt><dd>' + (r[2] ? r[1] : esc(r[1])) + '</dd>'; });
    html += '</dl><div class="detail-actions">' +
      '<button type="button" class="btn" data-action="copy" data-text="' + esc(m.virtual_ip) + '" data-label="IP address copied">' + icon('copy') + 'Copy IP</button>';
    if (n.is_owner && !m.is_self) {
      html += '<button type="button" class="btn danger" data-action="remove-member" data-id="' + esc(n.id) + '" data-key2="' + esc(m.public_key) + '">' + icon('trash') + 'Remove</button>';
    }
    return html + '</div></div>';
  }

  function subhead(title) {
    return '<div class="subhead" data-key="sub-' + esc(title) + '"><button type="button" class="icon-btn" data-action="back" title="Back" aria-label="Back">' + icon('back') +
      '</button><h1>' + esc(title) + '</h1></div>';
  }

  function createHTML() {
    var name = S.args.name != null ? S.args.name : F.defaultNetworkName(S.status.nickname);
    return subhead('Create a network') +
      '<form class="form" data-form="create" data-key="create" autocomplete="off">' +
      '<p class="lead">You own this network. Friends join it with an invite code you send them.</p>' +
      '<div class="field"><label for="cr-name">Network name</label>' +
      '<input class="input" id="cr-name" name="name" maxlength="64" value="' + esc(name) + '" data-autofocus spellcheck="false"></div>' +
      '<p class="hint">Only members see the name. You can rename it later.</p><div class="grow"></div>' +
      '<button type="submit" class="btn primary block"' + attr('disabled', S.busy.create) + '>' + (S.busy.create ? 'Creating…' : 'Create network') + '</button></form>';
  }

  function joinHTML() {
    var code = S.args.code || '';
    return subhead('Join a network') +
      '<form class="form" data-form="join" data-key="join" autocomplete="off">' +
      '<p class="lead">Paste the invite code a friend sent you.</p>' +
      '<div class="field"><label for="jn-code">Invite code</label>' +
      '<textarea class="textarea" id="jn-code" name="code" rows="4" spellcheck="false" placeholder="lanfolk-i1…" data-sync' + (code ? '' : ' data-autofocus') + '>' + esc(code) + '</textarea>' +
      (S.joinError ? '<p class="error-text" role="alert">' + esc(S.joinError) + '</p>' : (S.args.pasted ? '<p class="hint">Pasted from your clipboard.</p>' : '')) +
      '</div><div><button type="button" class="btn" data-action="paste">' + icon('paste') + 'Paste</button></div>' +
      '<div class="grow"></div>' +
      '<p class="hint" style="margin-bottom:12px">The network’s owner has to be online for you to join. Lanfolk keeps trying until they are.</p>' +
      '<button type="submit" class="btn primary block"' + attr('disabled', S.busy.join) + '>' + (S.busy.join ? 'Joining…' : 'Join network') + '</button></form>';
  }

  function inviteHTML() {
    var n = networkById(S.args.network_id);
    if (!n) return subhead('Invite friends') + '<div class="empty"><p>This network no longer exists.</p></div>';
    var inv = S.inviteCode && S.inviteCode.network_id === n.id ? S.inviteCode : null;
    var copied = S.copied === 'invite';
    return subhead('Invite friends') +
      '<div class="form" data-key="invite">' +
      '<p class="lead">Send this code to your friends to let them into <strong>' + esc(n.name) + '</strong>.</p>' +
      '<div class="code-box"><textarea class="textarea selectable" readonly rows="4" aria-label="Invite code" data-action-focus="select" data-sync>' +
      esc(inv ? inv.code : 'Creating the code…') + '</textarea></div>' +
      '<div class="validity"><span>' + esc(inv ? F.inviteValidity(inv.expires, Date.now()) : '') + '</span>' +
      (inv ? '<button type="button" class="link" data-action="reset-invite">Make a new code</button>' : '') + '</div>' +
      '<ol class="steps"><li>Send it in Discord, WhatsApp or any chat.</li>' +
      '<li>Your friend opens Lanfolk, chooses Join network and pastes it.</li>' +
      '<li>Stay online until they show up in your network.</li></ol>' +
      '<div class="grow"></div>' +
      '<button type="button" class="btn primary block" data-action="copy-invite"' + attr('disabled', !inv) + '>' +
      icon(copied ? 'check' : 'copy') + (copied ? 'Copied' : 'Copy invite') + '</button></div>';
  }

  function toggleHTML(name, on, label) {
    return '<label class="switch"><input type="checkbox" data-setting="' + name + '"' + attr('checked', on) + ' aria-label="' + esc(label) + '"><span class="track"></span>' +
      '<span class="state">' + (on ? 'On' : 'Off') + '</span></label>';
  }
  function rowHTML(title, desc, control, key) {
    return '<div class="row" data-key="' + key + '"><div class="row-text"><div class="row-title">' + esc(title) + '</div>' +
      (desc ? '<div class="row-desc">' + desc + '</div>' : '') + '</div>' + control + '</div>';
  }

  function settingsHTML() {
    var st = S.status;
    var s = S.settings;
    var html = subhead('Settings') + '<div class="scroll" data-key="settings"><div class="settings">';

    html += '<section class="group"><h2>Profile</h2><div class="card">' +
      rowHTML('Nickname', 'Friends and members see this name.',
        '<input class="input" data-field="nickname" maxlength="32" value="' + esc(st.nickname) + '" spellcheck="false" aria-label="Nickname">', 'nick') +
      rowHTML('Open at sign-in', 'Start Lanfolk in the notification area when you sign in to Windows.',
        S.autostart == null ? '' : toggleHTML('autostart', S.autostart, 'Open at sign-in'), 'auto') +
      '</div></section>';

    if (s) {
      html += '<section class="group"><h2>Games</h2><div class="card">' +
        rowHTML('Compatibility mode', 'Turn on if a game does not find the others. Sends all broadcast traffic through Lanfolk.',
          toggleHTML('compat_mode', s.compat_mode, 'Compatibility mode'), 'compat') +
        '</div></section>';

      html += '<section class="group"><h2>Connection</h2><div class="card">' +
        rowHTML('Relay only', 'Hides your IP address from members. Adds latency.', toggleHTML('relay_only', s.relay_only, 'Relay only'), 'ro') +
        rowHTML('Automatic port mapping', 'Ask your router (UPnP, NAT-PMP) to open a port for faster direct connections.', toggleHTML('port_mapping', s.port_mapping, 'Automatic port mapping'), 'pm') +
        rowHTML('Public DHT fallback', 'If no relay can be reached, find members through the BitTorrent DHT. Your IP address becomes visible there.', toggleHTML('dht_fallback', s.dht_fallback, 'Public DHT fallback'), 'dht') +
        rowHTML('Fixed UDP port', 'For a port you forwarded on your router. Leave empty for automatic.',
          '<input class="input narrow num" data-field="manual_port" inputmode="numeric" maxlength="5" placeholder="Auto" value="' + (s.manual_port ? s.manual_port : '') + '" aria-label="Fixed UDP port">', 'mp') +
        '</div></section>';

      html += '<section class="group"><h2>Relays</h2><div class="card"><div class="row row-stack" data-key="relays"><div class="row-text">' +
        '<div class="row-desc">Relays forward encrypted traffic when a direct connection is not possible. They cannot read it.</div></div>';
      (st.relays || []).forEach(function (r, i) {
        var up = r.mode === 'udp' || r.mode === 'tcp';
        html += '<div class="relay-line" data-key="r' + i + '"><span class="dot ' + (up ? 'online' : 'attention') + '"></span><span>' + esc(r.name) +
          ' <span class="hint" style="display:inline">· ' + esc(r.source) + '</span></span><span class="num">' +
          (up ? esc(F.relayModeLabel(r.mode).split(' ')[0] + ' · ' + F.formatMs(r.rtt_ms)) : 'Down') + '</span></div>';
      });
      (s.custom_relays || []).forEach(function (r, i) {
        html += '<div class="relay-line" data-key="c' + i + '"><span>Your relay: ' + esc(F.formatRelay(r)) + '</span>' +
          '<button type="button" class="link" style="margin-left:auto" data-action="remove-relay" data-index="' + i + '">Remove</button></div>';
      });
      html += '<form class="inline-form" data-form="relay" autocomplete="off">' +
        '<input class="input" name="host" placeholder="Add a relay: host" aria-label="Relay host" spellcheck="false">' +
        '<input class="input num" name="udp" value="3478" aria-label="UDP port" inputmode="numeric">' +
        '<input class="input num" name="tcp" value="443" aria-label="TCP port" inputmode="numeric">' +
        '<button type="submit" class="btn" style="height:30px">Add</button></form></div></div></section>';
    }

    html += '<section class="group"><h2>Friends</h2><div class="card"><div class="row row-stack" data-key="friends"><div class="row-text">' +
      '<div class="row-desc">Invites make people friends automatically. You can also exchange friend codes by hand.</div></div>' +
      '<div class="relay-line"><span class="mono selectable" style="flex:1">' + esc(st.friend_code) + '</span>' +
      '<button type="button" class="icon-btn" data-action="copy" data-text="' + esc(st.friend_code) + '" data-label="Friend code copied" title="Copy your friend code" aria-label="Copy your friend code">' + icon('copy') + '</button></div>';
    S.friends.forEach(function (f) {
      html += '<div class="friend" data-key="f-' + esc(f.public_key) + '"><span class="dot ' + (f.online ? 'online' : 'offline') + '"></span>' +
        '<div class="friend-text"><div>' + esc(f.nickname || 'Unnamed') + '</div><div class="friend-sub">' +
        esc(f.state === 'active' ? F.friendPresence(f, Date.now()) : 'Waiting for them to add you') + '</div></div>' +
        '<button type="button" class="icon-btn" data-action="remove-friend" data-key2="' + esc(f.public_key) + '" title="Remove friend" aria-label="Remove ' + esc(f.nickname) + '">' + icon('trash') + '</button></div>';
    });
    html += '<form class="inline-form" style="grid-template-columns:1fr auto" data-form="friend" autocomplete="off">' +
      '<input class="input" name="code" placeholder="Paste a friend code" aria-label="Friend code" spellcheck="false">' +
      '<button type="submit" class="btn" style="height:30px">Add</button></form></div></div></section>';

    html += '<section class="group"><h2>Identity</h2><div class="card">' +
      rowHTML('Back up your identity', 'A password-protected file. Restore it after reinstalling Windows so friends do not have to add you again.',
        '<button type="button" class="btn" data-action="backup">Back up…</button>', 'bk') +
      rowHTML('Restore from a backup', 'Replaces the identity on this computer.', '<button type="button" class="btn" data-action="restore">Restore…</button>', 'rs') +
      '</div></section>';

    html += '<section class="group"><h2>Troubleshooting</h2><div class="card">' +
      rowHTML('Diagnostic report', 'Connection details for a bug report. It contains no keys or passwords.',
        '<button type="button" class="btn" data-action="diagnostics">Save…</button>', 'dg') +
      rowHTML('Service', esc('UDP port ' + st.listen_port + (st.port_mapping ? ' · mapped by ' + st.port_mapping.toUpperCase() : '') + ' · DHT ' + F.dhtLabel(st.dht).split(' ')[0].toLowerCase()), '', 'svc') +
      '</div></section>';

    html += '<section class="group"><h2>About</h2><div class="card"><div class="row" data-key="about"><div class="about">' + mark(32) +
      '<div><div class="row-title">Lanfolk ' + esc(st.version) + '</div><div class="row-desc">Open source under the Apache License 2.0.</div>' +
      '<div class="about-links"><button type="button" class="link" data-action="url" data-url="https://github.com/NotrinoResearch/lanfolk">Project page</button>' +
      '<button type="button" class="link" data-action="url" data-url="https://github.com/NotrinoResearch/lanfolk/blob/main/THIRD_PARTY_NOTICES.md">Third-party notices</button></div>' +
      '</div></div></div></div></section>';

    return html + '</div></div>';
  }

  // --------------------------------------------------------------- actions --

  function go(view, args) {
    closeMenu();
    S.view = view;
    S.args = args || {};
    S.joinError = '';
    S.copied = null;
    if (view === 'invite') loadInvite(S.args.network_id, false);
    if (view === 'join') sniffClipboard();
    if (view === 'settings') loadSettings();
    render();
    var v = $('#view');
    if (v) v.scrollTop = 0;
  }

  function loadSettings() {
    call('GetSettings').then(function (s) { S.settings = s; render(); }, fail);
    call('StartWithWindows').then(function (on) { S.autostart = on; render(); }, function () { S.autostart = null; });
  }

  function loadInvite(id, reset) {
    if (!id) return;
    if (!reset && S.inviteCode && S.inviteCode.network_id === id && Date.parse(S.inviteCode.expires) > Date.now()) return;
    call('CreateInvite', id, !!reset).then(function (r) {
      S.inviteCode = { network_id: id, code: r.code, expires: r.expires };
      render();
    }, function (e) { fail(e); });
  }

  function sniffClipboard() {
    if (S.args.code || typeof rt.ClipboardGetText !== 'function') return;
    rt.ClipboardGetText().then(function (t) {
      if (S.view === 'join' && !S.args.code && F.looksLikeInvite(t)) {
        S.args.code = String(t).trim();
        S.args.pasted = true;
        render();
      }
    }, function () {});
  }

  function setSetting(name, value) {
    if (name === 'autostart') {
      S.autostart = value;
      render();
      return call('SetStartWithWindows', value).catch(function (e) { S.autostart = !value; render(); fail(e); });
    }
    if (!S.settings) return;
    var prev = S.settings;
    var next = Object.assign({}, prev);
    next[name] = value;
    S.settings = next;
    render();
    return call('SetSettings', next).then(function () {
      return call('GetSettings').then(function (s) { S.settings = s; render(); });
    }, function (e) { S.settings = prev; render(); fail(e); });
  }

  function togglePower() {
    var net = activeNetwork();
    if (net) {
      store.set('last-network', net.id);
      call('ActivateNetwork', '').catch(fail);
      return;
    }
    var id = store.get('last-network');
    if (!networkById(id)) id = sortedNetworks()[0] && sortedNetworks()[0].id;
    if (id) call('ActivateNetwork', id).catch(fail);
  }

  function confirmDialog(opts) {
    return new Promise(function (resolve) {
      var d = $('#dialog');
      d.innerHTML = '<form method="dialog"><div class="dialog-body"><h2>' + esc(opts.title) + '</h2><p>' + esc(opts.text) + '</p>' +
        (opts.input ? '<input class="input" name="v" ' + (opts.password ? 'type="password" ' : '') + 'value="' + esc(opts.value || '') + '" aria-label="' + esc(opts.input) + '" placeholder="' + esc(opts.input) + '" maxlength="64">' : '') +
        (opts.input2 ? '<input class="input" style="margin-top:8px" name="v2" type="password" aria-label="' + esc(opts.input2) + '" placeholder="' + esc(opts.input2) + '">' : '') +
        '</div><div class="dialog-foot"><button type="button" class="btn" value="cancel" data-close>Cancel</button>' +
        '<button type="submit" class="btn ' + (opts.danger ? 'primary' : 'primary') + '" value="ok">' + esc(opts.ok || 'OK') + '</button></div></form>';
      var done = false;
      function finish(v) { if (!done) { done = true; d.close(); resolve(v); } }
      d.querySelector('[data-close]').onclick = function () { finish(null); };
      d.querySelector('form').onsubmit = function (ev) {
        ev.preventDefault();
        var f = ev.target;
        finish(opts.input ? { v: f.v.value, v2: f.v2 ? f.v2.value : '' } : true);
      };
      d.oncancel = function () { finish(null); };
      d.showModal();
      var first = d.querySelector('input') || d.querySelector('[type=submit]');
      if (first) { first.focus(); if (first.select) first.select(); }
    });
  }

  // ---- network menu ----
  function openMenu(btn, net) {
    var m = $('#menu');
    var items = [];
    if (net.is_owner) items.push(['invite', 'invite', 'Invite friends…']);
    items.push(net.active ? ['disconnect', 'leave', 'Disconnect'] : ['connect', 'power', 'Connect']);
    if (net.is_owner) items.push(['rename', 'edit', 'Rename…']);
    items.push(['hr']);
    items.push(['leave', 'trash', net.is_owner ? 'Delete network…' : 'Leave network…', true]);
    m.innerHTML = items.map(function (it) {
      if (it[0] === 'hr') return '<hr>';
      return '<button type="button" role="menuitem" class="' + (it[3] ? 'danger' : '') + '" data-menu="' + it[0] + '" data-id="' + esc(net.id) + '">' + icon(it[1]) + esc(it[2]) + '</button>';
    }).join('');
    m.hidden = false;
    var r = btn.getBoundingClientRect();
    var w = m.offsetWidth;
    var h = m.offsetHeight;
    var top = r.bottom + 4;
    if (top + h > window.innerHeight - 8) top = r.top - h - 4;
    m.style.top = Math.max(8, top) + 'px';
    m.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, r.right - w)) + 'px';
    m.querySelector('button').focus();
  }
  function closeMenu() { var m = $('#menu'); if (m && !m.hidden) { m.hidden = true; m.innerHTML = ''; } }

  function menuAction(what, id) {
    closeMenu();
    var n = networkById(id);
    if (!n) return;
    switch (what) {
      case 'invite': go('invite', { network_id: id }); break;
      case 'connect': call('ActivateNetwork', id).catch(fail); break;
      case 'disconnect': store.set('last-network', id); call('ActivateNetwork', '').catch(fail); break;
      case 'rename':
        confirmDialog({ title: 'Rename network', text: 'Members see the new name the next time they connect.', input: 'Network name', value: n.name, ok: 'Rename' })
          .then(function (r) { if (r && r.v.trim()) call('RenameNetwork', id, r.v.trim()).catch(fail); });
        break;
      case 'leave':
        confirmDialog(n.is_owner
          ? { title: 'Delete ' + n.name + '?', text: 'You own this network. Deleting it removes it for good: nobody can join, and members can no longer change it.', ok: 'Delete', danger: true }
          : { title: 'Leave ' + n.name + '?', text: 'You will need a new invite to join again.', ok: 'Leave', danger: true })
          .then(function (ok) { if (ok) call('LeaveNetwork', id).catch(fail); });
        break;
    }
  }

  // ---------------------------------------------------------------- events --

  document.addEventListener('click', function (ev) {
    var menuBtn = ev.target.closest('[data-menu]');
    if (menuBtn) { menuAction(menuBtn.getAttribute('data-menu'), menuBtn.getAttribute('data-id')); return; }
    if (!ev.target.closest('#menu')) closeMenu();
    var el = ev.target.closest('[data-action]');
    if (!el || el.disabled) return;
    var a = el.getAttribute('data-action');
    var id = el.getAttribute('data-id');
    var key = el.getAttribute('data-key2');
    switch (a) {
      case 'retry': call('Retry'); break;
      case 'go': go(el.getAttribute('data-view')); break;
      case 'back': go('home'); break;
      case 'power': togglePower(); break;
      case 'copy': copy(el.getAttribute('data-text'), el.getAttribute('data-label')); break;
      case 'toggle-net':
        var n = networkById(id);
        var cur = S.open[id] != null ? S.open[id] : !!(n && n.active);
        S.open[id] = !cur;
        render();
        break;
      case 'connect': call('ActivateNetwork', id).catch(fail); break;
      case 'net-menu': ev.stopPropagation(); openMenu(el, networkById(id)); break;
      case 'invite': go('invite', { network_id: id }); break;
      case 'member':
        var k = id + '/' + key;
        S.detail = S.detail === k ? null : k;
        render();
        break;
      case 'remove-member':
        var net = networkById(id);
        var mem = net && net.members.filter(function (m) { return m.public_key === key; })[0];
        confirmDialog({ title: 'Remove ' + (mem ? mem.nickname : 'member') + '?', text: 'They lose access to ' + (net ? net.name : 'the network') + ' right away. If they still have a valid invite code, make a new code so it stops working.', ok: 'Remove', danger: true })
          .then(function (ok) { if (ok) call('RemoveMember', id, key).then(function () { S.detail = null; }, fail); });
        break;
      case 'cancel-join': call('CancelInvite', id).catch(fail); break;
      case 'paste':
        if (typeof rt.ClipboardGetText === 'function') {
          rt.ClipboardGetText().then(function (t) { S.args.code = String(t || '').trim(); S.args.pasted = false; S.joinError = ''; render(); var ta = $('#jn-code'); if (ta) ta.value = S.args.code; });
        }
        break;
      case 'copy-invite':
        if (S.inviteCode) {
          call('CopyToClipboard', S.inviteCode.code).then(function () {
            S.copied = 'invite';
            render();
            setTimeout(function () { if (S.copied === 'invite') { S.copied = null; render(); } }, 2400);
          }, fail);
        }
        break;
      case 'reset-invite':
        confirmDialog({ title: 'Make a new invite code?', text: 'The current code stops working. People who already joined stay in the network.', ok: 'Make new code' })
          .then(function (ok) { if (ok) loadInvite(S.args.network_id, true); });
        break;
      case 'remove-relay':
        var i = +el.getAttribute('data-index');
        var list = (S.settings.custom_relays || []).filter(function (_, j) { return j !== i; });
        setSetting('custom_relays', list);
        break;
      case 'remove-friend':
        var f = S.friends.filter(function (x) { return x.public_key === key; })[0];
        confirmDialog({ title: 'Remove ' + (f ? f.nickname : 'friend') + '?', text: 'Networks you share are not changed.', ok: 'Remove', danger: true })
          .then(function (ok) { if (ok) call('RemoveFriend', key).catch(fail); });
        break;
      case 'backup': doBackup(); break;
      case 'restore': doRestore(); break;
      case 'diagnostics':
        call('ExportDiagnostics').then(function (p) { if (p) toast('Report saved', 'ok'); }, fail);
        break;
      case 'url': if (typeof rt.BrowserOpenURL === 'function') rt.BrowserOpenURL(el.getAttribute('data-url')); break;
    }
  });

  document.addEventListener('focusin', function (ev) {
    if (ev.target.matches && ev.target.matches('[data-action-focus="select"]')) ev.target.select();
  });

  document.addEventListener('change', function (ev) {
    var t = ev.target;
    var name = t.getAttribute('data-setting');
    if (name) { setSetting(name, t.checked); return; }
    var field = t.getAttribute('data-field');
    if (field === 'nickname') {
      var v = t.value.trim();
      if (v && v !== S.status.nickname) call('SetNickname', v).then(function () { toast('Nickname saved', 'ok'); }, fail);
    } else if (field === 'manual_port') {
      var txt = t.value.trim();
      if (txt === '') { setSetting('manual_port', 0); return; }
      var e = F.validatePort(txt);
      if (e) { toast(e, 'bad'); return; }
      setSetting('manual_port', Number(txt));
    }
  });

  document.addEventListener('input', function (ev) {
    if (ev.target.id === 'jn-code') { S.args.code = ev.target.value; S.args.pasted = false; if (S.joinError) { S.joinError = ''; render(); } }
    if (ev.target.id === 'cr-name') S.args.name = ev.target.value;
  });

  document.addEventListener('submit', function (ev) {
    var f = ev.target;
    var kind = f.getAttribute('data-form');
    if (!kind) return;
    ev.preventDefault();
    if (kind === 'onboard') {
      var nick = f.nick.value.trim();
      var done = function () { store.set('onboarded', '1'); render(); };
      if (nick && nick !== S.status.nickname) call('SetNickname', nick).then(done, fail);
      else done();
    } else if (kind === 'create') {
      var name = f.name.value.trim();
      if (!name) { toast('Give the network a name.', 'bad'); return; }
      S.busy.create = true;
      render();
      call('CreateNetwork', name).then(function (n) {
        S.busy.create = false;
        store.set('onboarded', '1');
        var id = n && n.id;
        if (!id) { go('home'); return; }
        S.networks = S.networks.concat([n]).filter(function (x, i, arr) { return arr.findIndex(function (y) { return y.id === x.id; }) === i; });
        go('invite', { network_id: id });
      }, function (e) { S.busy.create = false; render(); fail(e); });
    } else if (kind === 'join') {
      var code = f.code.value.trim();
      if (!F.looksLikeInvite(code)) {
        S.joinError = code ? 'That does not look like an invite code. It starts with lanfolk-i1.' : 'Paste the invite code first.';
        render();
        return;
      }
      S.busy.join = true;
      render();
      call('RedeemInvite', code).then(function () {
        S.busy.join = false;
        store.set('onboarded', '1');
        go('home');
      }, function (e) { S.busy.join = false; S.joinError = errText(e); render(); });
    } else if (kind === 'relay') {
      var form = { host: f.host.value, udp: f.udp.value, tcp: f.tcp.value };
      var err = F.validateRelay(form);
      if (err) { toast(err, 'bad'); return; }
      setSetting('custom_relays', (S.settings.custom_relays || []).concat([F.relayFromForm(form)]));
      f.host.value = '';
    } else if (kind === 'friend') {
      var c = f.code.value.trim();
      if (!c) return;
      call('AddFriend', c, '').then(function () { f.code.value = ''; toast('Friend added. They have to add you too.', 'ok'); }, fail);
    }
  });

  document.addEventListener('keydown', function (ev) {
    if (ev.key === 'Escape') {
      if (!$('#menu').hidden) { closeMenu(); return; }
      if ($('#dialog').open) return;
      if (S.view !== 'home') { go('home'); return; }
    }
    if (!$('#menu').hidden && (ev.key === 'ArrowDown' || ev.key === 'ArrowUp')) {
      var items = Array.prototype.slice.call($('#menu').querySelectorAll('button'));
      var i = items.indexOf(document.activeElement);
      var n = ev.key === 'ArrowDown' ? (i + 1) % items.length : (i - 1 + items.length) % items.length;
      items[n].focus();
      ev.preventDefault();
    }
  });

  function doBackup() {
    confirmDialog({ title: 'Back up your identity', text: 'Choose a password. You need it to restore the backup; nobody can recover it.', input: 'Password', password: true, input2: 'Repeat the password', ok: 'Continue' })
      .then(function (r) {
        if (!r) return;
        if (r.v.length < 8) { toast('Use at least 8 characters.', 'bad'); return; }
        if (r.v !== r.v2) { toast('The passwords do not match.', 'bad'); return; }
        return call('ExportIdentity', r.v).then(function (backup) {
          return call('SaveTextFile', 'Save identity backup', 'lanfolk-identity-backup.txt', backup);
        }).then(function (p) { if (p) toast('Backup saved', 'ok'); }, fail);
      });
  }
  function doRestore() {
    call('OpenTextFile', 'Choose a Lanfolk identity backup').then(function (text) {
      if (!text) return;
      return confirmDialog({ title: 'Restore this identity?', text: 'Your current identity on this computer is replaced. Friends who know only the current one must add you again.', input: 'Backup password', password: true, ok: 'Restore', danger: true })
        .then(function (r) {
          if (!r) return;
          return call('ImportIdentity', text, r.v).then(function () { toast('Identity restored', 'ok'); });
        });
    }).catch(fail);
  }

  // ------------------------------------------------------------ title bar --

  $('#tb-brand').innerHTML = mark(16) + '<span>Lanfolk</span>';
  $('#tb-settings').innerHTML = icon('settings');
  $('#tb-min').innerHTML = icon('minimize');
  $('#tb-close').innerHTML = icon('close');
  $('#tb-settings').addEventListener('click', function () { go(S.view === 'settings' ? 'home' : 'settings'); });
  $('#tb-min').addEventListener('click', function () { if (rt.WindowMinimise) rt.WindowMinimise(); });
  $('#tb-close').addEventListener('click', function () { if (rt.WindowHide) rt.WindowHide(); });
  $('#titlebar').addEventListener('dblclick', function (ev) {
    if (!ev.target.closest('button') && rt.WindowToggleMaximise) rt.WindowToggleMaximise();
  });

  // --------------------------------------------------------------- service --

  function on(name, fn) { if (typeof rt.EventsOn === 'function') rt.EventsOn(name, fn); }
  on('service', function (s) {
    S.service = s;
    if (!s.connected) { S.status = null; S.peers = []; }
    render();
  });
  on('status', function (s) { S.status = s; render(); });
  on('networks', function (n) {
    S.networks = n || [];
    if (S.detail) {
      var parts = S.detail.split('/');
      var net = networkById(parts[0]);
      if (!net || !net.members.some(function (m) { return m.public_key === parts[1]; })) S.detail = null;
    }
    render();
  });
  on('peers', function (p) { S.peers = p || []; render(); });
  on('friends', function (f) { S.friends = f || []; render(); });
  on('invites', function (j) { S.invites = j || []; render(); });
  on('navigate', function (v) {
    if (!v || !S.service || !S.service.connected) return;
    if (v.view === 'invite' && v.network_id) go('invite', { network_id: v.network_id });
    else if (v.view === 'settings') go('settings');
  });

  render();
})();
