// Lanfolk desktop UI. Plain JavaScript, no build step.
//
// One page, two windows: the compact main window ("main") and the Settings
// window ("settings", the same executable started with --settings). The Go
// side (ui/app.go) is bound as window.go.main.App and forwards every call to
// the Lanfolk service. State arrives as events: "service", "status",
// "networks", "peers", "friends", "invites", plus "navigate" from the tray.
//
// Rendering: each view is a function that returns an HTML string; render()
// morphs the live DOM toward it, so focus, scroll and typed text survive the
// once-a-second peer updates. Clicks are handled by delegation on
// data-action attributes; actions on a member or friend live in a context
// menu (the ⋯ button or a right click), never inside the row.
(function () {
  'use strict';

  var F = window.LanfolkFormat;
  var api = (window.go && window.go.main && window.go.main.App) || {};
  var rt = window.runtime || {};

  var store = {
    get: function (k) { try { return window.localStorage.getItem('lanfolk.' + k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem('lanfolk.' + k, v); } catch (e) { /* storage off */ } },
  };

  // Where friends without Lanfolk download it (in the invite message).
  var DOWNLOAD_URL = 'https://github.com/NotrinoResearch/lanfolk/releases';
  // A service that just went away is usually restarting (an update, a
  // reboot of the service): show "Reconnecting" this long before "not running".
  var RECONNECT_GRACE = 30000;
  var STARTUP_GRACE = 20000; // the window opened before the service started
  var CONNECT_HINT_AFTER = 20000; // explain a slow adapter start after this

  var S = {
    mode: null, // "main" | "settings"
    service: null, // {connected, message} once known
    serviceSeen: false, // connected at least once
    serviceLostAt: 0,
    startedAt: Date.now(),
    connectingSince: 0,
    update: null, // UpdateInfo from the update check
    portError: '',
    portText: '',
    status: null,
    networks: [],
    peers: [],
    friends: [],
    invites: [],
    settings: null,
    autostart: null,
    view: 'home', // main: home | create | join | invite | add-friend
    tab: 'networks', // home tab: networks | friends
    page: 'general', // settings window page
    args: {},
    open: {}, // network id -> expanded
    detail: null, // "netId/key" or "friend/key" whose details are open
    busy: {},
    inviteCode: null,
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

  // busy runs a call while S.busy[key] is set, so its button shows that it
  // is working and cannot be pressed twice.
  function busy(key, promise) {
    S.busy[key] = true;
    render();
    function done() { S.busy[key] = false; render(); }
    return Promise.resolve(promise).then(function (v) { done(); return v; }, function (e) { done(); throw e; });
  }

  var toastTimer;
  function toast(msg, kind) {
    var box = $('#toasts');
    var bad = kind === 'bad';
    box.innerHTML = '<div class="toast' + (bad ? ' bad' : '') + '"' + (bad ? ' role="alert"' : '') + ' data-action="dismiss-toast" title="Dismiss">' +
      (kind === 'ok' ? icon('check') : bad ? icon('warning') : '') + '<span>' + esc(msg) + '</span></div>';
    clearTimeout(toastTimer);
    // Errors stay long enough to read a full sentence.
    toastTimer = setTimeout(function () { box.innerHTML = ''; }, bad ? Math.max(8000, 60 * String(msg).length) : 2400);
  }
  function fail(e) { toast(errText(e), 'bad'); }
  function copy(text, label) {
    return call('CopyToClipboard', text).then(function () { toast(label || 'Copied', 'ok'); }, fail);
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
      morph(view, S.mode === 'settings' ? settingsWindowHTML() : mainHTML());
      var gear = $('#tb-settings');
      gear.hidden = S.mode !== 'main' || !S.service || !S.service.connected || isOnboarding();
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
  function friendByKey(k) {
    for (var i = 0; i < S.friends.length; i++) if (S.friends[i].public_key === k) return S.friends[i];
    return null;
  }
  function memberOf(net, key) {
    var ms = (net && net.members) || [];
    for (var i = 0; i < ms.length; i++) if (ms[i].public_key === key) return ms[i];
    return null;
  }
  function openJoins() {
    return S.invites.filter(function (j) {
      if (j.state === 'joined') return false;
      return !(j.state === 'expired' && networkById(j.network_id));
    });
  }
  function isOnboarding() {
    return !!(S.mode === 'main' && S.service && S.service.connected && S.status && !store.get('onboarded') &&
      S.networks.length === 0 && openJoins().length === 0);
  }
  function selfState() {
    var net = activeNetwork();
    if (!net) return { dot: 'offline', text: 'Offline' };
    if (S.status && S.status.adapter_error) return { dot: 'attention', text: 'Network adapter problem' };
    if (S.status && !S.status.adapter_up) return { dot: 'connecting', text: 'Connecting to ' + net.name + '…' };
    return { dot: 'online', text: 'Online in ' + net.name };
  }
  function sortedNetworks() {
    return S.networks.slice().sort(function (a, b) {
      if (a.active !== b.active) return a.active ? -1 : 1;
      return String(a.name).localeCompare(String(b.name), undefined, { sensitivity: 'base' });
    });
  }
  function ownedNetworksWithout(key) {
    return sortedNetworks().filter(function (n) { return n.is_owner && !memberOf(n, key); });
  }
  function friendsOnline() {
    return S.friends.filter(function (f) { return f.online; }).length;
  }

  // ---------------------------------------------------------- main window --

  function loadingHTML() { return '<div class="empty" data-key="loading"><span class="spinner"></span></div>'; }

  var graceTimer;
  // While the service is (re)starting the window says so calmly instead of
  // "not running"; render() is scheduled again for when the grace ends.
  function serviceGraceLeft() {
    var now = Date.now();
    var until = S.serviceSeen ? S.serviceLostAt + RECONNECT_GRACE : S.startedAt + STARTUP_GRACE;
    return Math.max(0, until - now);
  }
  function serviceDownHTML() {
    var left = serviceGraceLeft();
    if (left > 0) {
      clearTimeout(graceTimer);
      graceTimer = setTimeout(render, left + 50);
      return reconnectingHTML();
    }
    return offlineHTML();
  }
  function reconnectingHTML() {
    return '<div class="empty mute" data-key="reconnecting"><span class="spinner" aria-hidden="true"></span>' +
      '<h2 class="gap-top">' + (S.serviceSeen ? 'Reconnecting…' : 'Starting…') + '</h2>' +
      '<p>' + (S.serviceSeen ? 'The Lanfolk service is restarting. This takes a few seconds, for example after an update.' : 'Waiting for the Lanfolk service.') + '</p></div>';
  }

  function mainHTML() {
    if (!S.mode || !S.service) return loadingHTML();
    if (!S.service.connected) return serviceDownHTML();
    if (!S.status) return loadingHTML();
    if (isOnboarding() && S.view === 'home') return onboardingHTML();
    switch (S.view) {
      case 'create': return createHTML();
      case 'join': return joinHTML();
      case 'invite': return inviteHTML();
      case 'add-friend': return addFriendHTML();
      default: return homeHTML();
    }
  }

  function offlineHTML() {
    return '<div class="empty mute" data-key="offline">' + mark(56) +
      '<h2>The Lanfolk service is not running</h2>' +
      '<p>Choose Try again to start it. It also starts with Windows. If it does not start, restart the computer; if that does not help, install Lanfolk again. Your networks and friends are kept.' +
      (S.service.message ? ' ' + esc(S.service.message) : '') + '</p>' +
      '<div class="stack"><button type="button" class="btn" data-action="retry"' + attr('disabled', S.busy.retry) + '>' +
      (S.busy.retry ? '<span class="spinner sm" aria-hidden="true"></span>Trying…' : icon('refresh') + 'Try again') + '</button></div>' +
      '<p class="hint gap-top">This window reconnects by itself.</p></div>';
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
      '</div><p class="note">' + icon('info') + '<span>Lanfolk keeps running in the notification area when you close this window.</span></p></div>';
  }

  function homeHTML() {
    var me = selfState();
    var net = activeNetwork();
    var ip = S.status.virtual_ip;
    var html = '<section class="me" data-key="me">' +
      '<button type="button" class="power' + (net ? ' on' : '') + (S.busy.power ? ' busy' : '') + '" data-action="power" aria-pressed="' + !!net + '"' +
      attr('disabled', S.networks.length === 0 || S.busy.power) +
      ' title="' + (S.networks.length === 0 ? 'Create or join a network first' : net ? 'Go offline' : 'Go online') + '" aria-label="Online">' + icon('power') + '</button>' +
      '<div class="me-text"><div class="me-name">' + esc(S.status.nickname) + '</div>' +
      '<div class="me-status"><span class="dot ' + me.dot + '"></span><span>' + esc(me.text) + '</span></div></div>' +
      (ip ? '<button type="button" class="ip-chip" data-action="copy" data-text="' + esc(ip) + '" data-label="IP address copied" title="Copy your IP address">' + esc(ip) + icon('copy') + '</button>' : '') +
      '</section>';
    html += bannersHTML();

    var joins = openJoins();
    if (S.networks.length === 0 && joins.length === 0 && S.friends.length === 0) {
      return html + '<div class="empty" data-key="empty">' + mark(44) +
        '<h2>No networks yet</h2><p>Create a network and send your friends the invite, or join with an invite someone sent you.</p>' +
        '<div class="stack"><button type="button" class="btn primary" data-action="go" data-view="create">' + icon('plus') + 'Create a network</button>' +
        '<button type="button" class="btn" data-action="go" data-view="join">' + icon('join') + 'Join with an invite</button></div></div>';
    }

    var fo = friendsOnline();
    html += '<div class="tabs" role="tablist" data-key="tabs">' +
      tabHTML('networks', 'Networks', '') +
      tabHTML('friends', 'Friends', S.friends.length ? String(fo) + ' online' : '') + '</div>';

    if (S.tab === 'friends') {
      html += '<div class="scroll" data-key="friends-list" role="tabpanel" id="panel-friends" aria-labelledby="tab-friends"><div class="list">' + friendsHTML() + '</div></div>';
      html += '<footer class="actions" data-key="f-actions">' +
        '<button type="button" class="btn" data-action="copy" data-text="' + esc(S.status.friend_code) + '" data-label="Friend code copied">' + icon('copy') + 'My friend code</button>' +
        '<button type="button" class="btn primary" data-action="go" data-view="add-friend">' + icon('invite') + 'Add friend</button></footer>';
      return html;
    }

    html += '<div class="scroll" data-key="list" role="tabpanel" id="panel-networks" aria-labelledby="tab-networks"><div class="list">';
    html += playTipHTML();
    joins.forEach(function (j) { html += pendingHTML(j); });
    if (S.networks.length === 0) {
      html += '<p class="list-empty">You are not in a network yet.</p>';
    }
    sortedNetworks().forEach(function (n) { html += networkHTML(n); });
    html += '</div></div>';
    html += '<footer class="actions" data-key="actions">' +
      '<button type="button" class="btn" data-action="go" data-view="create">' + icon('plus') + 'Create</button>' +
      '<button type="button" class="btn primary" data-action="go" data-view="join">' + icon('join') + 'Join network</button></footer>';
    return html;
  }

  function tabHTML(id, label, meta) {
    var on = S.tab === id;
    return '<button type="button" role="tab" id="tab-' + id + '" aria-controls="panel-' + id + '" tabindex="' + (on ? '0' : '-1') + '" class="tab' + (on ? ' on' : '') + '" aria-selected="' + on + '" data-action="tab" data-id="' + id + '">' +
      esc(label) + (meta ? '<span class="tab-meta">' + esc(meta) + '</span>' : '') + '</button>';
  }

  // Banners, most important first; at most two so the list stays visible.
  function bannersHTML() {
    var st = S.status;
    var out = [];
    if (st.adapter_error) {
      out.push('<div class="banner bad" data-key="b-adapter" role="alert">' + icon('warning') +
        '<div><strong>The network adapter could not start.</strong> ' + esc(st.adapter_error) +
        ' Restarting the computer usually helps. <button type="button" class="link" data-action="diagnostics">Save a diagnostic report</button></div></div>');
    }
    var net = activeNetwork();
    if (net && !st.adapter_error && !st.adapter_up && S.connectingSince && Date.now() - S.connectingSince > CONNECT_HINT_AFTER) {
      out.push('<div class="banner" data-key="b-slow">' + icon('info') +
        '<div>Still connecting. Windows is setting up the Lanfolk network adapter, which can take up to a minute after an update. If it does not finish, restart the computer.</div></div>');
    }
    var up = (st.relays || []).some(function (r) { return r.mode === 'udp' || r.mode === 'tcp'; });
    if (net && (st.relays || []).length && !up) {
      out.push('<div class="banner" data-key="b-relay">' + icon('warning') +
        '<div>No relay server can be reached, so only friends with a direct connection can see you. Check your internet connection; a firewall that blocks UDP can also cause this.</div></div>');
    }
    (st.warnings || []).forEach(function (w, i) {
      if (st.adapter_error && /network adapter/i.test(w)) return; // shown above
      out.push('<div class="banner" data-key="b-w' + i + '">' + icon('info') + '<div>' + esc(w) + '</div></div>');
    });
    var u = S.update;
    if (u && u.available && store.get('dismissed-update') !== u.latest) {
      out.push('<div class="banner info" data-key="b-update">' + icon('info') +
        '<div class="grow">Lanfolk ' + esc(u.latest) + ' is available. <button type="button" class="link" data-action="url" data-url="' + esc(u.url || DOWNLOAD_URL) + '">Download</button></div>' +
        '<button type="button" class="icon-btn sm" data-action="dismiss-update" title="Hide until the next version" aria-label="Hide the update notice">' + icon('close') + '</button></div>');
    } else {
      var newer = newestMemberVersion();
      if (newer && store.get('dismissed-update') !== newer.version) {
        out.push('<div class="banner info" data-key="b-newer">' + icon('info') +
          '<div class="grow">' + esc(newer.nickname) + ' uses a newer Lanfolk (' + esc(newer.version) + '). <button type="button" class="link" data-action="url" data-url="' + DOWNLOAD_URL + '">Get it</button></div>' +
          '<button type="button" class="icon-btn sm" data-action="dismiss-update" data-version="' + esc(newer.version) + '" title="Hide" aria-label="Hide the update notice">' + icon('close') + '</button></div>');
      }
    }
    return out.slice(0, 2).join('');
  }

  // The member with the newest Lanfolk, if it is newer than ours.
  function newestMemberVersion() {
    var ours = S.status && S.status.version;
    var best = null;
    S.peers.forEach(function (p) {
      if (p.online && F.versionRelation(p.version, ours) === 'newer' && (!best || F.compareVersions(p.version, best.version) > 0)) best = p;
    });
    return best;
  }

  // A one-time tip once someone else is online in the active network.
  function playTipHTML() {
    var net = activeNetwork();
    if (!net || store.get('tip-play') || !S.peers.some(function (p) { return p.online && p.path && p.path !== 'none'; })) return '';
    return '<div class="tip" data-key="tip-play">' + icon('info') +
      '<div class="grow"><strong>Ready to play.</strong> Start the game and open its LAN or local multiplayer list. If it asks for an IP address, use the one under a name.</div>' +
      '<button type="button" class="icon-btn sm" data-action="dismiss-tip" title="Got it" aria-label="Hide this tip">' + icon('close') + '</button></div>';
  }

  function pendingHTML(j) {
    var name = j.network_name || (j.owner_nickname ? j.owner_nickname + '’s network' : 'A network');
    var spinning = j.state === 'waiting' || j.state === 'joining';
    return '<div class="pending" data-key="join-' + esc(j.network_id) + '">' +
      (spinning ? '<span class="spinner" aria-hidden="true"></span>' : icon('warning')) +
      '<div class="pending-text"><div class="pending-title">' + esc(name) + '</div>' +
      '<div class="pending-sub">' + esc(F.joinStateText(j)) + '</div></div>' +
      '<button type="button" class="btn sm" data-action="cancel-join" data-id="' + esc(j.network_id) + '">' +
      (j.state === 'expired' ? 'Dismiss' : 'Cancel') + '</button></div>';
  }

  function networkHTML(n) {
    var open = S.open[n.id] != null ? S.open[n.id] : n.active;
    var selfOn = n.active && selfState().dot === 'online';
    var count = F.onlineCount(n, n.active ? S.peers : [], selfOn);
    var meta;
    if (n.active) meta = count.others === 0 ? 'Only you so far' : count.online + ' of ' + count.total + ' online';
    else meta = F.plural(count.total, 'member');
    var html = '<div class="net' + (open ? ' open' : '') + (n.active ? ' active' : '') + '" data-key="net-' + esc(n.id) + '">' +
      '<div class="net-head">' +
      '<button type="button" class="net-toggle" data-action="toggle-net" data-id="' + esc(n.id) + '" aria-expanded="' + !!open + '">' +
      icon('chevron') + '<span class="net-name">' + esc(n.name) + '</span>' +
      '<span class="net-meta">' + esc(meta) + '</span></button>' +
      (n.active ? '' : '<button type="button" class="btn net-connect" data-action="connect" data-id="' + esc(n.id) + '"' + attr('disabled', S.busy['connect-' + n.id]) + '>' +
        (S.busy['connect-' + n.id] ? 'Connecting…' : 'Connect') + '</button>') +
      '<button type="button" class="icon-btn" data-action="net-menu" data-id="' + esc(n.id) + '" aria-haspopup="menu" title="More" aria-label="More options for ' + esc(n.name) + '">' + icon('more') + '</button>' +
      '</div>';
    if (open) {
      html += '<ul class="members">';
      membersOf(n).forEach(function (m) { html += memberHTML(n, m); });
      if (n.is_owner && n.members.length <= 1) {
        html += '<li class="member" data-key="invite-hint"><button type="button" class="member-row hint-row" data-action="invite" data-id="' + esc(n.id) + '">' +
          '<span></span><span class="m-name accent">Invite friends…</span><span></span></button></li>';
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

  function tagsHTML(m, p) {
    var t = [];
    if (m.is_self) t.push('You');
    if (m.is_owner) t.push('Owner');
    if (p && p.online && F.versionRelation(p.version, S.status && S.status.version) === 'older') t.push('Older Lanfolk');
    return t.length ? '<small>' + esc(t.join(' · ')) + '</small>' : '';
  }

  var DOT_TEXT = { online: 'Online', fair: 'Online, fair connection', poor: 'Online, poor connection', connecting: 'Connecting', offline: 'Offline', attention: 'Problem' };

  // A member is one row; selecting it extends the same row downwards with
  // the connection readout. Actions are in the row's menu.
  function memberHTML(n, m) {
    var key = m.public_key;
    var id = n.id + '/' + key;
    var open = S.detail === id;
    var dot = 'offline';
    var right = '';
    var p = n.active && !m.is_self ? peerByKey(key) : null;
    if (m.is_self) {
      dot = n.active ? selfState().dot : 'offline';
    } else if (n.active) {
      var sum = F.memberSummary(p);
      dot = sum.state;
      right = sum.state === 'offline' || sum.state === 'connecting'
        ? '<span class="m-right ' + sum.state + '">' + esc(sum.text) + '</span>'
        : '<span class="m-right">' + esc(sum.text) + '<small class="' + (sum.state !== 'online' ? 'q-' + sum.state : '') + '">' + esc(sum.sub) + '</small></span>';
    }
    var html = '<li class="member' + (open ? ' open' : '') + '" data-key="m-' + esc(key) + '" data-ctx="member" data-id="' + esc(n.id) + '" data-key2="' + esc(key) + '">' +
      '<div class="member-line">' +
      '<button type="button" class="member-row" data-action="member" data-id="' + esc(n.id) + '" data-key2="' + esc(key) + '" aria-expanded="' + open + '">' +
      '<span class="dot ' + dot + '" aria-hidden="true"></span><span class="sr-only">' + esc(DOT_TEXT[dot] || '') + ', </span>' +
      '<span class="m-name">' + esc(m.nickname || 'Unnamed') + tagsHTML(m, p) +
      '<span class="m-ip">' + esc(m.virtual_ip) + '</span></span>' + right + '</button>' +
      '<button type="button" class="icon-btn row-more" data-action="member-menu" data-id="' + esc(n.id) + '" data-key2="' + esc(key) + '" aria-haspopup="menu" title="More" aria-label="More options for ' + esc(m.nickname) + '">' + icon('more') + '</button>' +
      '</div>';
    if (open) html += memberDetailHTML(n, m);
    return html + '</li>';
  }

  function memberDetailHTML(n, m) {
    var p = n.active && !m.is_self ? peerByKey(m.public_key) : null;
    var cells = [];
    var advice = '';
    if (p && F.isPeerMeasured(p)) {
      var q = F.qualityBadge(p.quality);
      var via = F.relayDetail(p);
      cells.push(['Connection', F.pathLabel(p.path)]);
      if (via) cells.push(['Route', via.replace(/^via /, '')]);
      cells.push(['Average ping', F.formatMs(p.avg_ping_ms)]);
      cells.push(['Jitter', F.formatMs(p.jitter_ms)]);
      cells.push(['Packet loss', F.formatPercent(p.loss_pct)]);
      cells.push(['Quality', '<span class="q-' + q.cls + '">' + esc(q.label) + '</span>', true]);
      var rel = F.versionRelation(p.version, S.status && S.status.version);
      cells.push(['Lanfolk', p.version ? p.version + (rel === 'older' ? ' (older than yours)' : rel === 'newer' ? ' (newer than yours)' : '') : 'Older than 0.3.0']);
      advice = F.connectionAdvice(p);
      if (rel === 'older') advice = (advice ? advice + ' ' : '') + 'Ask ' + (m.nickname || 'them') + ' to update Lanfolk for the latest fixes.';
    } else if (m.is_self) {
      cells.push(['Status', n.active ? selfState().text : 'Not connected to this network']);
    } else if (n.active) {
      cells.push(['Status', p && p.online ? 'Connecting…' : 'Offline']);
      cells.push(['Seen', friendByKey(m.public_key) ? F.friendPresence(friendByKey(m.public_key), Date.now()) : '—']);
    } else {
      cells.push(['Status', 'Connect to this network to see them']);
    }
    var html = '<dl class="readout">';
    cells.forEach(function (c) {
      html += '<div><dt>' + esc(c[0]) + '</dt><dd>' + (c[2] ? c[1] : esc(c[1])) + '</dd></div>';
    });
    html += '</dl>';
    if (advice) html += '<p class="advice">' + esc(advice) + '</p>';
    return html;
  }

  function friendsHTML() {
    if (!S.friends.length) {
      return '<div class="empty small" data-key="f-empty"><h2>No friends yet</h2>' +
        '<p>People you share a network with become friends. You can also add someone with their friend code.</p></div>';
    }
    var list = S.friends.slice().sort(function (a, b) {
      if (!!a.online !== !!b.online) return a.online ? -1 : 1;
      if ((a.state === 'active') !== (b.state === 'active')) return a.state === 'active' ? -1 : 1;
      return String(a.nickname).localeCompare(String(b.nickname), undefined, { sensitivity: 'base' });
    });
    var html = '<ul class="members friends">';
    list.forEach(function (f) {
      var id = 'friend/' + f.public_key;
      var open = S.detail === id;
      var pending = f.state !== 'active';
      var sub = pending ? 'Waiting for them to add you' : F.friendPresence(f, Date.now());
      html += '<li class="member' + (open ? ' open' : '') + '" data-key="f-' + esc(f.public_key) + '" data-ctx="friend" data-key2="' + esc(f.public_key) + '">' +
        '<div class="member-line">' +
        '<button type="button" class="member-row" data-action="friend" data-key2="' + esc(f.public_key) + '" aria-expanded="' + open + '">' +
        '<span class="dot ' + (f.online ? 'online' : pending ? 'connecting' : 'offline') + '" aria-hidden="true"></span>' +
        '<span class="m-name">' + esc(f.nickname || 'Unnamed') + '<span class="m-ip">' + esc(sub) + '</span></span><span></span></button>' +
        '<button type="button" class="icon-btn row-more" data-action="friend-menu" data-key2="' + esc(f.public_key) + '" aria-haspopup="menu" title="More" aria-label="More options for ' + esc(f.nickname) + '">' + icon('more') + '</button>' +
        '</div>';
      if (open) {
        var shared = S.networks.filter(function (n) { return memberOf(n, f.public_key); }).map(function (n) { return n.name; });
        html += '<dl class="readout"><div><dt>Networks together</dt><dd>' + esc(shared.length ? shared.join(', ') : 'None yet') + '</dd></div>' +
          '<div><dt>Friends since</dt><dd>' + esc(F.isZeroTime(f.added_at) ? '—' : new Date(f.added_at).toLocaleDateString()) + '</dd></div></dl>';
      }
      html += '</li>';
    });
    return html + '</ul>';
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
      '<p class="lead">Paste the invite a friend sent you: the whole message or just the code.</p>' +
      '<div class="field"><label for="jn-code">Invite</label>' +
      '<textarea class="textarea" id="jn-code" name="code" rows="4" spellcheck="false" placeholder="lanfolk-i1…" data-sync' + (code ? '' : ' data-autofocus') + '>' + esc(code) + '</textarea>' +
      (S.joinError ? '<p class="error-text" role="alert">' + esc(S.joinError) + '</p>' : (S.args.pasted ? '<p class="hint">Pasted from your clipboard.</p>' : '')) +
      '</div><div><button type="button" class="btn" data-action="paste">' + icon('paste') + 'Paste</button></div>' +
      '<div class="grow"></div>' +
      '<p class="hint gap-bottom">The network’s owner has to be online for you to join. Lanfolk keeps trying until they are.</p>' +
      '<button type="submit" class="btn primary block"' + attr('disabled', S.busy.join) + '>' + (S.busy.join ? 'Joining…' : 'Join network') + '</button></form>';
  }

  function inviteHTML() {
    var n = networkById(S.args.network_id);
    if (!n) return subhead('Invite friends') + '<div class="empty"><p>This network no longer exists.</p></div>';
    var inv = S.inviteCode && S.inviteCode.network_id === n.id ? S.inviteCode : null;
    var copied = S.copied === 'invite';
    var known = S.friends.filter(function (f) { return f.state === 'active' && !memberOf(n, f.public_key); });
    return subhead('Invite friends') +
      '<div class="form" data-key="invite">' +
      '<p class="lead">Send this invite to your friends to let them into <strong>' + esc(n.name) + '</strong>.</p>' +
      '<div class="code-box"><textarea class="textarea selectable" readonly rows="4" aria-label="Invite code" data-action-focus="select" data-sync>' +
      esc(inv ? inv.code : 'Creating the code…') + '</textarea></div>' +
      '<div class="validity"><span>' + esc(inv ? F.inviteValidity(inv.expires, Date.now()) : '') + '</span>' +
      (inv ? '<button type="button" class="link" data-action="reset-invite">Make a new code</button>' : '') + '</div>' +
      '<ol class="steps"><li>Copy the invite and send it in Discord, WhatsApp or any chat. It includes the download link for friends without Lanfolk.</li>' +
      '<li>Your friend chooses Join network in Lanfolk and pastes the whole message.</li>' +
      '<li>Stay online until they show up in your network.</li></ol>' +
      (known.length ? '<p class="hint gap-top">Already friends? Add them directly from the Friends tab.</p>' : '') +
      '<div class="grow"></div>' +
      '<button type="button" class="btn primary block" data-action="copy-invite"' + attr('disabled', !inv) + '>' +
      icon(copied ? 'check' : 'copy') + (copied ? 'Copied' : 'Copy invite message') + '</button></div>';
  }

  function addFriendHTML() {
    return subhead('Add a friend') +
      '<form class="form" data-form="friend" data-key="add-friend" autocomplete="off">' +
      '<p class="lead">Paste your friend’s friend code. They have to add yours too before you can play.</p>' +
      '<div class="field"><label for="af-code">Their friend code</label>' +
      '<textarea class="textarea" id="af-code" name="code" rows="3" spellcheck="false" placeholder="lanfolk1…" data-autofocus></textarea></div>' +
      '<div class="field"><label for="af-nick">Name (optional)</label>' +
      '<input class="input" id="af-nick" name="nick" maxlength="32" spellcheck="false" placeholder="Shown until they connect"></div>' +
      '<div class="field"><label>Your friend code</label><div class="code-line"><span class="mono selectable grow">' + esc(S.status.friend_code) + '</span>' +
      '<button type="button" class="icon-btn" data-action="copy" data-text="' + esc(S.status.friend_code) + '" data-label="Friend code copied" title="Copy your friend code" aria-label="Copy your friend code">' + icon('copy') + '</button></div></div>' +
      '<div class="grow"></div>' +
      '<button type="submit" class="btn primary block"' + attr('disabled', S.busy.friend) + '>' + (S.busy.friend ? 'Adding…' : 'Add friend') + '</button></form>';
  }

  // ------------------------------------------------------- settings window --

  var PAGES = [
    ['general', 'settings', 'General'],
    ['connection', 'power', 'Connection'],
    ['relays', 'join', 'Relays'],
    ['identity', 'invite', 'Identity and backup'],
    ['troubleshooting', 'warning', 'Troubleshooting'],
    ['about', 'info', 'About'],
  ];

  function settingsWindowHTML() {
    if (!S.mode || !S.service) return loadingHTML();
    if (!S.service.connected) return serviceDownHTML();
    if (!S.status) return loadingHTML();
    var nav = '<nav class="side" aria-label="Settings pages" data-key="side">';
    PAGES.forEach(function (p) {
      var on = S.page === p[0];
      nav += '<button type="button" class="side-item' + (on ? ' on' : '') + '" data-action="page" data-id="' + p[0] + '"' + (on ? ' aria-current="page"' : '') + '>' +
        icon(p[1]) + '<span>' + esc(p[2]) + '</span></button>';
    });
    nav += '</nav>';
    var title = '';
    PAGES.forEach(function (p) { if (p[0] === S.page) title = p[2]; });
    return '<div class="settings-shell" data-key="shell">' + nav +
      '<div class="scroll pane" data-key="pane-' + S.page + '"><div class="pane-inner"><h1 class="pane-title">' + esc(title) + '</h1>' + pageHTML(S.page) + '</div></div></div>';
  }

  function toggleHTML(name, on, label) {
    return '<label class="switch"><input type="checkbox" data-setting="' + name + '"' + attr('checked', on) + ' aria-label="' + esc(label) + '"><span class="track"></span>' +
      '<span class="state">' + (on ? 'On' : 'Off') + '</span></label>';
  }
  function rowHTML(title, desc, control, key) {
    return '<div class="row" data-key="' + key + '"><div class="row-text"><div class="row-title">' + esc(title) + '</div>' +
      (desc ? '<div class="row-desc">' + desc + '</div>' : '') + '</div>' + control + '</div>';
  }

  function pageHTML(page) {
    var st = S.status;
    var s = S.settings;
    switch (page) {
      case 'general':
        return '<div class="card">' +
          rowHTML('Nickname', 'Friends and members see this name.',
            '<input class="input" data-field="nickname" maxlength="32" value="' + esc(st.nickname) + '" spellcheck="false" aria-label="Nickname">', 'nick') +
          rowHTML('Open at sign-in', 'Start Lanfolk in the notification area when you sign in to Windows.',
            S.autostart == null ? '' : toggleHTML('autostart', S.autostart, 'Open at sign-in'), 'auto') +
          (s ? rowHTML('Compatibility mode', 'Turn on if a game does not find the others. Sends all broadcast traffic through Lanfolk.',
            toggleHTML('compat_mode', s.compat_mode, 'Compatibility mode'), 'compat') : '') +
          (s ? rowHTML('Notifications', 'When a friend comes online or joins your network. Windows keeps them quiet during full-screen games.',
            toggleHTML('notifications', s.notifications, 'Notifications'), 'notif') : '') +
          (s ? rowHTML('Check for updates', 'Once a day Lanfolk asks GitHub for the newest version number. Nothing else is sent.',
            toggleHTML('check_updates', s.check_updates, 'Check for updates'), 'upd') : '') +
          '</div>';
      case 'connection':
        if (!s) return loadingHTML();
        return '<div class="card">' +
          rowHTML('Relay only', 'Hides your IP address from members. Adds latency.', toggleHTML('relay_only', s.relay_only, 'Relay only'), 'ro') +
          rowHTML('Automatic port mapping', 'Ask your router (UPnP, NAT-PMP) to open a port for faster direct connections.', toggleHTML('port_mapping', s.port_mapping, 'Automatic port mapping'), 'pm') +
          rowHTML('Public DHT fallback', 'If no relay can be reached, find members through the BitTorrent DHT. Your IP address becomes visible there.', toggleHTML('dht_fallback', s.dht_fallback, 'Public DHT fallback'), 'dht') +
          rowHTML('Fixed UDP port', 'For a port you forwarded on your router. Leave empty for automatic. Takes effect after the service restarts.' +
            (S.portError ? '<span class="error-text block" id="mp-err" role="alert">' + esc(S.portError) + '</span>' : ''),
            '<input class="input narrow num' + (S.portError ? ' invalid' : '') + '" data-field="manual_port" inputmode="numeric" maxlength="5" placeholder="Auto" value="' + esc(S.portError ? S.portText : (s.manual_port ? s.manual_port : '')) + '" aria-label="Fixed UDP port"' +
            (S.portError ? ' aria-invalid="true" aria-describedby="mp-err"' : '') + '>', 'mp') +
          '</div><p class="pane-note">Lanfolk listens on UDP port ' + esc(st.listen_port) + (st.port_mapping ? ', mapped by your router (' + esc(st.port_mapping.toUpperCase()) + ')' : '') + '.</p>';
      case 'relays': {
        if (!s) return loadingHTML();
        var html = '<p class="pane-lead">Relays forward encrypted traffic when two members cannot reach each other directly. They cannot read it. The network owner can add relays for their network; you can add your own.</p><div class="card">';
        (st.relays || []).forEach(function (r, i) {
          var up = r.mode === 'udp' || r.mode === 'tcp';
          html += '<div class="row" data-key="r' + i + '"><span class="dot ' + (up ? 'online' : 'attention') + '"></span><div class="row-text"><div class="row-title">' + esc(r.name) + '</div>' +
            '<div class="row-desc">' + esc({ default: 'Built in', network: 'From the network owner', custom: 'Added by you' }[r.source] || r.source) + '</div></div>' +
            '<span class="num muted">' + (up ? esc(F.relayModeLabel(r.mode).split(' ')[0] + ' · ' + F.formatMs(r.rtt_ms)) : 'Down') + '</span></div>';
        });
        html += '</div><h2 class="pane-sub">Your relays</h2><div class="card">';
        (s.custom_relays || []).forEach(function (r, i) {
          html += '<div class="row" data-key="c' + i + '"><div class="row-text"><div class="row-title">' + esc(F.formatRelay(r)) + '</div></div>' +
            '<button type="button" class="btn sm" data-action="remove-relay" data-index="' + i + '">Remove</button></div>';
        });
        html += '<form class="row inline-form" data-form="relay" autocomplete="off">' +
          '<input class="input" name="host" placeholder="Host name or IP address" aria-label="Relay host" spellcheck="false">' +
          '<input class="input num" name="udp" value="3478" aria-label="UDP port" inputmode="numeric">' +
          '<input class="input num" name="tcp" value="443" aria-label="TCP port" inputmode="numeric">' +
          '<button type="submit" class="btn sm">Add</button></form></div>';
        return html;
      }
      case 'identity':
        return '<div class="card">' +
          rowHTML('Friend code', '<span class="mono selectable">' + esc(st.friend_code) + '</span>',
            '<button type="button" class="btn sm" data-action="copy" data-text="' + esc(st.friend_code) + '" data-label="Friend code copied">' + icon('copy') + 'Copy</button>', 'fc') +
          rowHTML('Back up your identity', 'A password-protected file. Restore it after reinstalling Windows so friends do not have to add you again.',
            '<button type="button" class="btn" data-action="backup">Back up…</button>', 'bk') +
          rowHTML('Restore from a backup', 'Replaces the identity on this computer.', '<button type="button" class="btn" data-action="restore">Restore…</button>', 'rs') +
          '</div>';
      case 'troubleshooting':
        return '<div class="card">' +
          rowHTML('Diagnostic report', 'Connection details for a bug report, without keys, passwords, friend codes or full IP addresses.',
            '<button type="button" class="btn" data-action="diagnostics">Save…</button>', 'dg') +
          (s ? rowHTML('Let members reach Windows services', 'Members can always reach games and other programs you run. Programs that run as Windows services, such as remote access, media and database servers, stay out of reach. Turn this on only if a game server runs as a service.',
            toggleHTML('allow_services', s.allow_services, 'Let members reach Windows services'), 'svcs') : '') +
          rowHTML('Service', esc('Version ' + st.version + ' · UDP port ' + st.listen_port + ' · DHT ' + F.dhtLabel(st.dht).split(' ')[0].toLowerCase()), '', 'svc') +
          '</div><h2 class="pane-sub">Use a connection code</h2>' +
          '<p class="pane-lead">When a friend cannot reach you at all (for example the relay is down), they can create a connection code for you from their member or friend menu. Paste it here; it works for a few minutes.</p>' +
          '<div class="card"><form class="row inline-form two" data-form="conncode" autocomplete="off">' +
          '<input class="input" name="code" placeholder="lanfolk-c1…" aria-label="Connection code" spellcheck="false">' +
          '<button type="submit" class="btn sm"' + attr('disabled', S.busy.conncode) + '>' + (S.busy.conncode ? 'Trying…' : 'Use code') + '</button></form></div>';
      case 'about':
        return '<div class="card"><div class="row" data-key="about"><div class="about">' + mark(40) +
          '<div><div class="row-title">Lanfolk ' + esc(st.version) + '</div><div class="row-desc">Source available under the PolyForm Noncommercial License 1.0.0. Free for personal use.</div>' +
          '<div class="row-desc gap-top-sm" data-key="upd-state">' + updateStateHTML() + '</div>' +
          '<div class="about-links"><button type="button" class="link" data-action="url" data-url="https://github.com/NotrinoResearch/lanfolk">Project page</button>' +
          '<button type="button" class="link" data-action="url" data-url="https://github.com/NotrinoResearch/lanfolk/blob/main/THIRD_PARTY_NOTICES.md">Third-party notices</button></div>' +
          '</div></div></div></div>';
    }
    return '';
  }

  function updateStateHTML() {
    var u = S.update;
    var check = '<button type="button" class="link" data-action="check-updates"' + attr('disabled', S.busy.update) + '>' + (S.busy.update ? 'Checking…' : 'Check for updates') + '</button>';
    if (!u) return check;
    if (u.available) {
      return '<strong>Lanfolk ' + esc(u.latest) + ' is available.</strong> <button type="button" class="link" data-action="url" data-url="' + esc(u.url || DOWNLOAD_URL) + '">Download</button>' +
        '<br><span class="muted">Run the new installer over this one; your networks and friends are kept.</span>';
    }
    if (u.latest) return 'You have the newest version. ' + check;
    return esc(u.error ? 'Could not check for updates (' + u.error + '). ' : '') + check;
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
    render();
  }

  function openSettings() { call('OpenSettings').catch(fail); }

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
    }, fail);
  }

  function sniffClipboard() {
    if (S.args.code || typeof rt.ClipboardGetText !== 'function') return;
    rt.ClipboardGetText().then(function (t) {
      var found = F.extractInvite(t);
      if (S.view === 'join' && !S.args.code && found) {
        S.args.code = found;
        S.args.pasted = true;
        render();
        // The field has the focus, so render() does not overwrite it.
        var ta = $('#jn-code');
        if (ta && !ta.value.trim()) ta.value = found;
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
      busy('power', call('ActivateNetwork', '')).catch(fail);
      return;
    }
    var id = store.get('last-network');
    if (!networkById(id)) id = sortedNetworks()[0] && sortedNetworks()[0].id;
    if (id) activate(id);
  }

  function activate(id) {
    return busy('power', busy('connect-' + id, call('ActivateNetwork', id))).catch(fail);
  }

  function confirmDialog(opts) {
    return new Promise(function (resolve) {
      var d = $('#dialog');
      d.innerHTML = '<form method="dialog"><div class="dialog-body"><h2>' + esc(opts.title) + '</h2><p>' + esc(opts.text) + '</p>' +
        (opts.input ? '<input class="input" name="v" ' + (opts.password ? 'type="password" ' : '') + 'value="' + esc(opts.value || '') + '" aria-label="' + esc(opts.input) + '" placeholder="' + esc(opts.input) + '" maxlength="64">' : '') +
        (opts.input2 ? '<input class="input gap-top-sm" name="v2" type="password" aria-label="' + esc(opts.input2) + '" placeholder="' + esc(opts.input2) + '">' : '') +
        '</div><div class="dialog-foot">' + (opts.noCancel ? '' : '<button type="button" class="btn" value="cancel" data-close>Cancel</button>') +
        '<button type="submit" class="btn primary" value="ok">' + esc(opts.ok || 'OK') + '</button></div></form>';
      var done = false;
      function finish(v) { if (!done) { done = true; d.close(); resolve(v); } }
      var cancel = d.querySelector('[data-close]');
      if (cancel) cancel.onclick = function () { finish(null); };
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

  function codeDialog(code, expires) {
    var d = $('#dialog');
    var left = F.formatExpiry(expires, Date.now());
    d.innerHTML = '<form method="dialog"><div class="dialog-body"><h2>Connection code</h2>' +
      '<p>Send this to your friend when nothing else connects you. They paste it under Settings, Troubleshooting. ' + esc(left) + '.</p>' +
      '<textarea class="textarea selectable" readonly rows="4" aria-label="Connection code" data-action-focus="select">' + esc(code) + '</textarea>' +
      '</div><div class="dialog-foot"><button type="button" class="btn" data-close>Close</button>' +
      '<button type="submit" class="btn primary">' + icon('copy') + 'Copy</button></div></form>';
    d.querySelector('[data-close]').onclick = function () { d.close(); };
    d.querySelector('form').onsubmit = function (ev) { ev.preventDefault(); copy(code, 'Connection code copied'); d.close(); };
    d.showModal();
    d.querySelector('[type=submit]').focus();
  }

  // ---- context menus ----
  // items: [action, icon, label, danger?] or ['hr']; data goes to menuAction.
  var menuData = null;
  var menuReturn = null; // focus goes back here when the menu closes by keyboard or choice
  function openMenu(anchor, items, data) {
    var m = $('#menu');
    menuData = data;
    menuReturn = anchor && anchor.focus ? anchor : document.activeElement;
    m.innerHTML = items.map(function (it, i) {
      if (it[0] === 'hr') return '<hr>';
      return '<button type="button" role="menuitem" class="' + (it[3] ? 'danger' : '') + '" data-menu="' + esc(it[0]) + '" data-arg="' + esc(it[4] || '') + '">' + icon(it[1]) + '<span>' + esc(it[2]) + '</span></button>';
    }).join('');
    m.hidden = false;
    var w = m.offsetWidth;
    var h = m.offsetHeight;
    var x, y;
    if (anchor.x != null) { x = anchor.x; y = anchor.y; } else {
      var r = anchor.getBoundingClientRect();
      x = r.right - w;
      y = r.bottom + 4;
      if (y + h > window.innerHeight - 8) y = r.top - h - 4;
    }
    m.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, x)) + 'px';
    m.style.top = Math.max(8, Math.min(window.innerHeight - h - 8, y)) + 'px';
    var first = m.querySelector('button');
    if (first) first.focus();
  }
  function closeMenu(restoreFocus) {
    var m = $('#menu');
    if (!m || m.hidden) return;
    m.hidden = true;
    m.innerHTML = '';
    menuData = null;
    var back = menuReturn;
    menuReturn = null;
    if (restoreFocus && back && document.contains(back)) back.focus();
  }

  function networkMenu(anchor, net) {
    var items = [];
    if (net.is_owner) items.push(['invite', 'invite', 'Invite friends…']);
    items.push(net.active ? ['disconnect', 'leave', 'Disconnect'] : ['connect', 'power', 'Connect']);
    if (net.is_owner) items.push(['rename', 'edit', 'Rename…']);
    items.push(['hr']);
    items.push(['leave', 'trash', net.is_owner ? 'Delete network…' : 'Leave network…', true]);
    openMenu(anchor, items, { net: net.id });
  }
  function memberMenu(anchor, netId, key) {
    var n = networkById(netId);
    var m = memberOf(n, key);
    if (!n || !m) return;
    var items = [['copy-ip', 'copy', 'Copy IP address  ' + m.virtual_ip]];
    var open = S.detail === netId + '/' + key;
    items.push(['details', 'info', open ? 'Hide details' : 'Show details']);
    if (!m.is_self) items.push(['conncode', 'join', 'Create connection code…']);
    if (n.is_owner && !m.is_self) { items.push(['hr']); items.push(['remove-member', 'trash', 'Remove from ' + n.name + '…', true]); }
    openMenu(anchor, items, { net: netId, key: key });
  }
  function friendMenu(anchor, key) {
    var f = friendByKey(key);
    if (!f) return;
    var items = [];
    if (f.state === 'active') {
      ownedNetworksWithout(key).forEach(function (n) { items.push(['add-to', 'plus', 'Add to ' + n.name, false, n.id]); });
    }
    items.push(['copy-code', 'copy', 'Copy friend code']);
    if (f.state === 'active') items.push(['conncode', 'join', 'Create connection code…']);
    items.push(['hr']);
    items.push(['remove-friend', 'trash', 'Remove friend…', true]);
    openMenu(anchor, items, { key: key });
  }

  function menuAction(what, arg) {
    var d = menuData || {};
    closeMenu(true);
    var n = d.net ? networkById(d.net) : null;
    switch (what) {
      case 'invite': go('invite', { network_id: d.net }); break;
      case 'connect': activate(d.net); break;
      case 'disconnect': store.set('last-network', d.net); call('ActivateNetwork', '').catch(fail); break;
      case 'rename':
        if (!n) return;
        confirmDialog({ title: 'Rename network', text: 'Members who are online see the new name right away, the others when they connect.', input: 'Network name', value: n.name, ok: 'Rename' })
          .then(function (r) { if (r && r.v.trim()) call('RenameNetwork', d.net, r.v.trim()).catch(fail); });
        break;
      case 'leave':
        if (!n) return;
        confirmDialog(n.is_owner
          ? { title: 'Delete ' + n.name + '?', text: 'You own this network. Deleting it removes it for good: nobody can join, and members can no longer change it.', ok: 'Delete' }
          : { title: 'Leave ' + n.name + '?', text: 'You will need a new invite to join again.', ok: 'Leave' })
          .then(function (ok) { if (ok) call('LeaveNetwork', d.net).catch(fail); });
        break;
      case 'copy-ip': {
        var m = memberOf(n, d.key);
        if (m) copy(m.virtual_ip, 'IP address copied');
        break;
      }
      case 'details':
        S.detail = S.detail === d.net + '/' + d.key ? null : d.net + '/' + d.key;
        render();
        break;
      case 'remove-member': {
        var mem = memberOf(n, d.key);
        confirmDialog({ title: 'Remove ' + (mem ? mem.nickname : 'member') + '?', text: 'They lose access to ' + (n ? n.name : 'the network') + ' right away. The current invite code stops working too; send a new one to anyone who has not joined yet.', ok: 'Remove' })
          .then(function (ok) { if (ok) call('RemoveMember', d.net, d.key).then(function () { S.detail = null; }, fail); });
        break;
      }
      case 'add-to': {
        var target = networkById(arg);
        call('AddMember', arg, d.key).then(function () { toast('Added to ' + (target ? target.name : 'the network'), 'ok'); }, fail);
        break;
      }
      case 'conncode':
        call('CreateConnCode', d.key).then(function (r) { codeDialog(r.code, r.expires); }, fail);
        break;
      case 'copy-code': {
        var fr = friendByKey(d.key);
        if (fr) copy(fr.friend_code, 'Friend code copied');
        break;
      }
      case 'remove-friend': {
        var f = friendByKey(d.key);
        confirmDialog({ title: 'Remove ' + (f ? f.nickname : 'friend') + '?', text: 'Networks you share are not changed.', ok: 'Remove' })
          .then(function (ok) { if (ok) call('RemoveFriend', d.key).catch(fail); });
        break;
      }
    }
  }

  // ---------------------------------------------------------------- events --

  document.addEventListener('click', function (ev) {
    var menuBtn = ev.target.closest('[data-menu]');
    if (menuBtn) { menuAction(menuBtn.getAttribute('data-menu'), menuBtn.getAttribute('data-arg')); return; }
    if (!ev.target.closest('#menu')) closeMenu();
    var el = ev.target.closest('[data-action]');
    if (!el || el.disabled) return;
    var a = el.getAttribute('data-action');
    var id = el.getAttribute('data-id');
    var key = el.getAttribute('data-key2');
    switch (a) {
      case 'retry':
        // The window reconnects by itself; the button just does it now.
        busy('retry', Promise.all([call('Retry'), new Promise(function (r) { setTimeout(r, 1500); })])).catch(function () {});
        break;
      case 'dismiss-toast': $('#toasts').innerHTML = ''; break;
      case 'dismiss-update': {
        var v = el.getAttribute('data-version') || (S.update && S.update.latest);
        if (v) store.set('dismissed-update', v);
        render();
        break;
      }
      case 'dismiss-tip': store.set('tip-play', '1'); render(); break;
      case 'check-updates':
        busy('update', call('CheckForUpdates')).then(function (u) { S.update = u; render(); }, fail);
        break;
      case 'go': go(el.getAttribute('data-view')); break;
      case 'back': go('home'); break;
      case 'tab': S.tab = id; S.detail = null; render(); break;
      case 'page': S.page = id; render(); break;
      case 'power': togglePower(); break;
      case 'copy': copy(el.getAttribute('data-text'), el.getAttribute('data-label')); break;
      case 'toggle-net': {
        var n = networkById(id);
        var cur = S.open[id] != null ? S.open[id] : !!(n && n.active);
        S.open[id] = !cur;
        render();
        break;
      }
      case 'connect': activate(id); break;
      case 'net-menu': ev.stopPropagation(); networkMenu(el, networkById(id)); break;
      case 'member-menu': ev.stopPropagation(); memberMenu(el, id, key); break;
      case 'friend-menu': ev.stopPropagation(); friendMenu(el, key); break;
      case 'invite': go('invite', { network_id: id }); break;
      case 'member': {
        var k = id + '/' + key;
        S.detail = S.detail === k ? null : k;
        render();
        break;
      }
      case 'friend': {
        var fk = 'friend/' + key;
        S.detail = S.detail === fk ? null : fk;
        render();
        break;
      }
      case 'cancel-join': call('CancelInvite', id).catch(fail); break;
      case 'paste':
        if (typeof rt.ClipboardGetText === 'function') {
          rt.ClipboardGetText().then(function (t) {
            S.args.code = F.extractInvite(t) || String(t || '').trim();
            S.args.pasted = false;
            S.joinError = '';
            render();
            var ta = $('#jn-code');
            if (ta) ta.value = S.args.code;
          });
        }
        break;
      case 'copy-invite':
        if (S.inviteCode) {
          var inet = networkById(S.inviteCode.network_id);
          call('CopyToClipboard', F.inviteMessage(S.inviteCode.code, inet ? inet.name : 'Lanfolk', DOWNLOAD_URL)).then(function () {
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
      case 'remove-relay': {
        var i = +el.getAttribute('data-index');
        setSetting('custom_relays', (S.settings.custom_relays || []).filter(function (_, j) { return j !== i; }));
        break;
      }
      case 'backup': doBackup(); break;
      case 'restore': doRestore(); break;
      case 'diagnostics':
        call('ExportDiagnostics').then(function (p) { if (p) toast('Report saved', 'ok'); }, fail);
        break;
      case 'url': if (typeof rt.BrowserOpenURL === 'function') rt.BrowserOpenURL(el.getAttribute('data-url')); break;
    }
  });

  // Right click on a member or friend opens its menu at the pointer.
  document.addEventListener('contextmenu', function (ev) {
    var row = ev.target.closest('[data-ctx]');
    if (ev.target.closest('input, textarea')) return;
    ev.preventDefault();
    if (!row) return;
    var at = { x: ev.clientX, y: ev.clientY };
    if (row.getAttribute('data-ctx') === 'member') memberMenu(at, row.getAttribute('data-id'), row.getAttribute('data-key2'));
    else friendMenu(at, row.getAttribute('data-key2'));
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
      var e = txt === '' ? '' : F.validatePort(txt);
      S.portError = e;
      S.portText = txt;
      if (e) { render(); return; } // the field keeps what was typed
      setSetting('manual_port', txt === '' ? 0 : Number(txt));
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
        if (!n || !n.id) { go('home'); return; }
        if (!networkById(n.id)) S.networks = S.networks.concat([n]);
        go('invite', { network_id: n.id });
      }, function (e) { S.busy.create = false; render(); fail(e); });
    } else if (kind === 'join') {
      var typed = f.code.value.trim();
      var code = F.extractInvite(typed);
      if (!code) {
        S.joinError = typed ? 'There is no invite code in that text. It starts with lanfolk-i1.' : 'Paste the invite first.';
        render();
        return;
      }
      S.busy.join = true;
      render();
      call('RedeemInvite', code).then(function () {
        S.busy.join = false;
        store.set('onboarded', '1');
        S.tab = 'networks';
        go('home');
      }, function (e) { S.busy.join = false; S.joinError = errText(e); render(); });
    } else if (kind === 'relay') {
      var form = { host: f.host.value, udp: f.udp.value, tcp: f.tcp.value };
      var err = F.validateRelay(form);
      if (err) { toast(err, 'bad'); return; }
      setSetting('custom_relays', (S.settings.custom_relays || []).concat([F.relayFromForm(form)]));
      f.host.value = '';
    } else if (kind === 'conncode') {
      var cc = f.code.value.trim();
      if (!cc) { toast('Paste the connection code first.', 'bad'); return; }
      if (S.busy.conncode) return;
      busy('conncode', call('UseConnCode', cc)).then(function () { f.code.value = ''; toast('Trying the addresses in the code', 'ok'); }, fail);
    } else if (kind === 'friend') {
      var c = f.code.value.trim();
      if (!c) { toast('Paste their friend code first.', 'bad'); return; }
      if (S.busy.friend) return;
      busy('friend', call('AddFriend', c, f.nick ? f.nick.value.trim() : '')).then(function () {
        toast('Friend added. They have to add you too.', 'ok');
        S.tab = 'friends';
        go('home');
      }, fail);
    }
  });

  document.addEventListener('keydown', function (ev) {
    var menu = $('#menu');
    if (ev.key === 'Escape') {
      if (!menu.hidden) { closeMenu(true); return; }
      if ($('#dialog').open) return;
      if (S.mode === 'main' && S.view !== 'home') { go('home'); return; }
    }
    // Arrow keys move between tabs (WAI-ARIA tabs pattern).
    if (ev.target.getAttribute && ev.target.getAttribute('role') === 'tab' && (ev.key === 'ArrowRight' || ev.key === 'ArrowLeft' || ev.key === 'Home' || ev.key === 'End')) {
      var tabs = Array.prototype.slice.call(document.querySelectorAll('[role="tab"]'));
      var ti = tabs.indexOf(ev.target);
      var tn = ev.key === 'Home' ? 0 : ev.key === 'End' ? tabs.length - 1 : (ti + (ev.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
      ev.preventDefault();
      S.tab = tabs[tn].getAttribute('data-id');
      S.detail = null;
      render();
      requestAnimationFrame(function () { var t = $('#tab-' + S.tab); if (t) t.focus(); });
      return;
    }
    if (!menu.hidden && (ev.key === 'ArrowDown' || ev.key === 'ArrowUp')) {
      var items = Array.prototype.slice.call(menu.querySelectorAll('button'));
      var i = items.indexOf(document.activeElement);
      var n = ev.key === 'ArrowDown' ? (i + 1) % items.length : (i - 1 + items.length) % items.length;
      items[n].focus();
      ev.preventDefault();
    }
    // Shift+F10 and the context-menu key open the focused row's menu.
    if ((ev.key === 'ContextMenu' || (ev.key === 'F10' && ev.shiftKey)) && document.activeElement) {
      var row = document.activeElement.closest('[data-ctx]');
      if (row) {
        ev.preventDefault();
        var more = row.querySelector('.row-more');
        if (row.getAttribute('data-ctx') === 'member') memberMenu(more, row.getAttribute('data-id'), row.getAttribute('data-key2'));
        else friendMenu(more, row.getAttribute('data-key2'));
      }
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
      return confirmDialog({ title: 'Restore this identity?', text: 'Your current identity on this computer is replaced. Friends who know only the current one must add you again.', input: 'Backup password', password: true, ok: 'Restore' })
        .then(function (r) {
          if (!r) return;
          return call('ImportIdentity', text, r.v).then(function () { toast('Identity restored', 'ok'); });
        });
    }).catch(fail);
  }

  // ------------------------------------------------------------ title bar --

  function setupTitlebar() {
    var settings = S.mode === 'settings';
    $('#tb-brand').innerHTML = mark(16) + '<span>' + (settings ? 'Lanfolk Settings' : 'Lanfolk') + '</span>';
    $('#tb-settings').innerHTML = icon('settings');
    $('#tb-min').innerHTML = icon('minimize');
    $('#tb-close').innerHTML = icon('close');
    $('#tb-close').title = settings ? 'Close' : 'Close to the notification area';
    $('#tb-close').setAttribute('aria-label', $('#tb-close').title);
    document.title = settings ? 'Lanfolk Settings' : 'Lanfolk';
    document.body.classList.toggle('is-settings', settings);
  }
  $('#tb-settings').addEventListener('click', openSettings);
  $('#tb-min').addEventListener('click', function () { if (rt.WindowMinimise) rt.WindowMinimise(); });
  $('#tb-close').addEventListener('click', function () {
    if (S.mode === 'settings') { if (rt.Quit) rt.Quit(); return; }
    function hide() { if (rt.WindowHide) rt.WindowHide(); }
    if (store.get('closed-once') || !S.service || !S.service.connected) { hide(); return; }
    store.set('closed-once', '1');
    confirmDialog({ title: 'Lanfolk keeps running', noCancel: true, ok: 'Got it',
      text: 'You stay online so friends can reach you. Open Lanfolk again from its icon in the notification area (it may be behind the ^ arrow). To go offline, press the power button first.' })
      .then(hide);
  });
  $('#titlebar').addEventListener('dblclick', function (ev) {
    if (!ev.target.closest('button') && rt.WindowToggleMaximise) rt.WindowToggleMaximise();
  });

  // --------------------------------------------------------------- service --

  function on(name, fn) { if (typeof rt.EventsOn === 'function') rt.EventsOn(name, fn); }
  on('service', function (s) {
    var was = S.service && S.service.connected;
    S.service = s;
    if (s.connected) S.serviceSeen = true;
    if (!s.connected && was) S.serviceLostAt = Date.now();
    if (!s.connected) { S.status = null; S.peers = []; }
    if (s.connected && !was && S.mode === 'settings') loadSettings();
    render();
  });
  on('status', function (s) {
    S.status = s;
    var connecting = !!(activeNetwork() && !s.adapter_up && !s.adapter_error);
    if (connecting && !S.connectingSince) {
      S.connectingSince = Date.now();
      setTimeout(render, CONNECT_HINT_AFTER + 100); // show the hint if it is still slow
    }
    if (!connecting) S.connectingSince = 0;
    render();
  });
  on('update', function (u) { S.update = u; render(); });
  on('networks', function (n) {
    S.networks = n || [];
    if (S.detail && S.detail.indexOf('friend/') !== 0) {
      var parts = S.detail.split('/');
      if (!memberOf(networkById(parts[0]), parts[1])) S.detail = null;
    }
    render();
  });
  on('peers', function (p) { S.peers = p || []; render(); });
  on('friends', function (f) { S.friends = f || []; render(); });
  on('invites', function (j) { S.invites = j || []; render(); });
  on('navigate', function (v) {
    if (!v || !S.service || !S.service.connected || S.mode !== 'main') return;
    if (v.view === 'invite' && v.network_id) go('invite', { network_id: v.network_id });
  });

  // The window may have been hidden or minimized: fetch fresh state when it
  // comes back (the per-second peer updates pause while it is hidden).
  var lastResync = 0;
  window.addEventListener('focus', function () {
    if (Date.now() - lastResync < 2000) return;
    lastResync = Date.now();
    call('Resync').catch(function () {});
  });

  call('Mode').then(function (m) { S.mode = m === 'settings' ? 'settings' : 'main'; }, function () { S.mode = 'main'; })
    .then(function () {
      setupTitlebar();
      render();
      // Events sent before this script ran are lost; ask for the current state.
      return call('Resync');
    }).catch(function () {});
  render();
})();
