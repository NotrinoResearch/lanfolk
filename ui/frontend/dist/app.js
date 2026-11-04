/* Lanfolk UI. Plain JavaScript, no framework and no build step.
 * Go methods:  window.go.main.App.<Method>()
 * Go events:   window.runtime.EventsOn(name, callback)
 * All text that comes from other people (nicknames, relay names) is put into
 * the page with textContent, never as HTML.
 */
(function () {
  'use strict';

  var F = window.LanfolkFormat;

  var state = {
    connected: null, // null until the first answer from the backend
    message: '',
    status: null,
    friends: [],
    networks: [],
    peers: [],
    settings: null,
    view: 'network',
  };

  var VIEWS = [
    { id: 'network', label: 'Network' },
    { id: 'friends', label: 'Friends' },
    { id: 'networks', label: 'Networks' },
    { id: 'settings', label: 'Settings' },
  ];

  // ---------------------------------------------------------------- helpers

  /** Creates an element. Children can be strings, nodes, arrays or null. */
  function h(tag, props) {
    var el = document.createElement(tag);
    var key;
    for (key in props || {}) {
      var val = props[key];
      if (val === null || val === undefined || val === false) continue;
      if (key === 'class') el.className = val;
      else if (key.indexOf('on') === 0 && typeof val === 'function') el.addEventListener(key.slice(2), val);
      else if (key === 'value' || key === 'checked' || key === 'disabled' || key === 'hidden' || key === 'selected') el[key] = val;
      else el.setAttribute(key, val === true ? '' : String(val));
    }
    for (var i = 2; i < arguments.length; i++) append(el, arguments[i]);
    return el;
  }

  function append(el, kid) {
    if (kid === null || kid === undefined || kid === false) return;
    if (Array.isArray(kid)) kid.forEach(function (k) { append(el, k); });
    else if (kid instanceof Node) el.appendChild(kid);
    else el.appendChild(document.createTextNode(String(kid)));
  }

  function $(sel) {
    return document.querySelector(sel);
  }

  function backend() {
    return window.go && window.go.main && window.go.main.App;
  }

  function errText(e) {
    if (typeof e === 'string') return e;
    if (e && e.message) return e.message;
    return 'Something went wrong. Please try again.';
  }

  /** Calls a Go method. Rejects with an Error whose message is readable. */
  function call(method) {
    var args = Array.prototype.slice.call(arguments, 1);
    var b = backend();
    if (!b || typeof b[method] !== 'function') {
      return Promise.reject(new Error('The Lanfolk app is not ready yet.'));
    }
    return b[method].apply(b, args).catch(function (e) {
      throw new Error(errText(e));
    });
  }

  function toast(text, kind) {
    var t = h('div', { class: 'toast' + (kind === 'error' ? ' error' : '') }, text);
    $('#toasts').appendChild(t);
    setTimeout(function () { t.remove(); }, kind === 'error' ? 9000 : 4500);
  }

  /** Runs fn while the button is disabled; shows errors as toasts. */
  function run(btn, fn, okText) {
    if (btn) btn.disabled = true;
    return Promise.resolve()
      .then(fn)
      .then(function (value) {
        if (okText) toast(okText);
        return { ok: true, value: value };
      })
      .catch(function (e) {
        toast(errText(e), 'error');
        return { ok: false };
      })
      .then(function (r) {
        if (btn) btn.disabled = false;
        return r;
      });
  }

  function copyText(text, what) {
    return call('CopyToClipboard', text).then(
      function () { toast(what + ' copied.'); },
      function (e) { toast(errText(e), 'error'); }
    );
  }

  function field(label, input, hint) {
    return h('label', { class: 'field' }, label, input, hint ? h('small', null, hint) : null);
  }

  function textInput(props) {
    return h('input', Object.assign({ type: 'text', autocomplete: 'off', spellcheck: 'false' }, props));
  }

  function badge(text, cls) {
    return h('span', { class: 'badge ' + (cls || 'neutral') }, text);
  }

  function dot(on) {
    return h('span', { class: 'dot' + (on ? ' on' : ''), 'aria-hidden': 'true' });
  }

  function emptyNote(text) {
    return h('p', { class: 'muted' }, text);
  }

  // ----------------------------------------------------------------- dialogs

  var dialogCounter = 0;

  /**
   * Opens a modal dialog. build(close) returns {title, body, actions, focus}.
   * The promise resolves with the value passed to close() (undefined on Esc).
   */
  function openModal(build) {
    return new Promise(function (resolve) {
      var titleId = 'dlg-title-' + ++dialogCounter;
      var dlg = h('dialog', { 'aria-labelledby': titleId });
      var result;
      var cleanups = [];
      function close(v) {
        result = v;
        if (dlg.open) dlg.close();
      }
      var parts = build(close, function (fn) { cleanups.push(fn); });
      dlg.appendChild(
        h('div', { class: 'dlg' }, h('h2', { id: titleId }, parts.title), parts.body, h('div', { class: 'dlg-actions' }, parts.actions))
      );
      dlg.addEventListener('close', function () {
        cleanups.forEach(function (fn) { fn(); });
        dlg.remove();
        resolve(result);
      });
      document.body.appendChild(dlg);
      dlg.showModal();
      if (parts.focus) parts.focus.focus();
    });
  }

  function confirmDialog(opts) {
    return openModal(function (close) {
      var confirmBtn = h('button', { type: 'button', class: opts.danger ? 'danger' : 'primary', onclick: function () { close(true); } }, opts.confirm || 'OK');
      return {
        title: opts.title,
        body: [].concat(opts.message).map(function (m) { return h('p', null, m); }),
        actions: [h('button', { type: 'button', onclick: function () { close(false); } }, 'Cancel'), confirmBtn],
        focus: opts.danger ? null : confirmBtn,
      };
    }).then(function (v) { return v === true; });
  }

  // ------------------------------------------------------------------- views

  var views = {};

  // ---- Network (home)

  function buildNetworkView() {
    var v = {};
    v.banners = h('div');
    v.summary = h('div', { class: 'card summary' });
    v.tbody = h('tbody');
    v.tableCard = h('div', { class: 'card table-card' },
      h('div', { class: 'table-wrap' },
        h('table', { 'aria-label': 'Members of the active network and the state of each connection' },
          h('thead', null, h('tr', null,
            h('th', { scope: 'col' }, 'Member'),
            h('th', { scope: 'col' }, 'Virtual IP'),
            h('th', { scope: 'col' }, 'Connection'),
            h('th', { scope: 'col', class: 'num' }, 'Ping'),
            h('th', { scope: 'col', class: 'num' }, 'Jitter'),
            h('th', { scope: 'col', class: 'num' }, 'Loss'),
            h('th', { scope: 'col' }, 'Quality'))),
          v.tbody)),
      h('p', { class: 'muted', style: 'margin:.7rem 0 0' },
        'Ping shows the latest round trip and the 60-second average. Jitter and loss are measured over 60 seconds.'));
    v.root = h('section', { class: 'view', id: 'view-network', 'aria-labelledby': 'h-network' },
      h('header', null, h('h1', { id: 'h-network', tabindex: '-1' }, 'Network')),
      v.banners, h('div', { class: 'stack' }, v.summary, v.tableCard));
    return v;
  }

  function activeNetwork() {
    var st = state.status;
    if (!st || !st.active_network_id) return null;
    return state.networks.find(function (n) { return n.id === st.active_network_id; }) || null;
  }

  function peerRows() {
    var st = state.status;
    var peers = F.sortPeers(state.peers);
    var known = {};
    peers.forEach(function (p) { known[p.public_key] = true; });
    var net = activeNetwork();
    if (net) {
      net.members.forEach(function (m) {
        if (m.is_self || known[m.public_key]) return;
        peers.push({ public_key: m.public_key, nickname: m.nickname, virtual_ip: m.virtual_ip, online: false, path: 'none', quality: 'unknown' });
      });
    }
    var rows = [];
    if (st && st.virtual_ip) {
      rows.push(h('tr', null,
        h('td', null, h('div', { class: 'who' }, dot(true), h('div', null, h('strong', null, st.nickname + ' (you)'), h('small', null, 'This computer')))),
        h('td', { class: 'ip' }, st.virtual_ip),
        h('td', null, '—'), h('td', { class: 'num' }, '—'), h('td', { class: 'num' }, '—'), h('td', { class: 'num' }, '—'), h('td', null, '—')));
    }
    peers.forEach(function (p) {
      var measured = F.isPeerMeasured(p);
      var q = F.qualityBadge(measured ? p.quality : 'unknown');
      var detail = F.relayDetail(p);
      rows.push(h('tr', { class: p.online ? '' : 'peer-offline' },
        h('td', null, h('div', { class: 'who' }, dot(p.online), h('div', null, h('strong', null, p.nickname), h('small', null, p.online ? 'Online' : 'Offline')))),
        h('td', { class: 'ip' }, p.virtual_ip),
        h('td', null, F.pathLabel(measured ? p.path : 'none'), detail ? h('small', null, detail) : null),
        h('td', { class: 'num' }, measured ? F.formatMs(p.ping_ms) : '—', measured && p.avg_ping_ms > 0 ? h('small', null, 'avg ' + F.formatMs(p.avg_ping_ms)) : null),
        h('td', { class: 'num' }, measured ? F.formatMs(p.jitter_ms) : '—'),
        h('td', { class: 'num' }, measured ? F.formatPercent(p.loss_pct) : '—'),
        h('td', null, measured ? badge(q.label, q.cls) : '—')));
    });
    return rows;
  }

  function updateNetworkView() {
    var v = views.network;
    var st = state.status;
    v.banners.replaceChildren();
    if (!st) {
      v.summary.replaceChildren(h('p', { class: 'muted' }, 'Loading...'));
      v.tableCard.hidden = true;
      return;
    }
    var active = !!st.active_network_id;
    if (st.adapter_error) {
      v.banners.appendChild(h('div', { class: 'banner error', role: 'alert' },
        h('p', null, 'The virtual network adapter could not be started: ' + st.adapter_error + '. Members cannot reach you until this is fixed.')));
    } else if (active && !st.adapter_up) {
      v.banners.appendChild(h('div', { class: 'banner warn' }, h('p', null, 'The virtual network adapter is starting. This usually takes a few seconds.')));
    }
    (st.warnings || []).forEach(function (w) {
      v.banners.appendChild(h('div', { class: 'banner warn' }, h('p', null, w)));
    });
    if (!active) {
      v.banners.appendChild(h('div', { class: 'banner info' },
        h('p', null, 'You are not in an active network. Activate a network, or create one and add your friends.'),
        h('button', { type: 'button', onclick: function () { showView('networks'); } }, 'Go to Networks')));
      v.summary.replaceChildren(h('div', null, h('div', { class: 'big' }, 'No active network'), h('small', null, 'Nobody can reach your computer through Lanfolk right now.')));
      v.tableCard.hidden = true;
      return;
    }
    var online = state.peers.filter(function (p) { return p.online; }).length;
    var total = state.peers.length;
    v.summary.replaceChildren(
      h('div', null, h('small', null, 'Active network'), h('div', { class: 'big' }, st.active_network_name)),
      h('div', null, h('small', null, 'Your virtual IP'),
        h('div', { class: 'actions' },
          h('span', { class: 'big ip' }, st.virtual_ip || '—'),
          st.virtual_ip ? h('button', { type: 'button', class: 'small', 'aria-label': 'Copy my virtual IP', onclick: function () { copyText(st.virtual_ip, 'Virtual IP'); } }, 'Copy') : null)),
      h('div', null, h('small', null, 'Members online'), h('div', { class: 'big' }, online + ' of ' + total)));
    v.tableCard.hidden = false;
    v.tbody.replaceChildren.apply(v.tbody, peerRows());
  }

  // ---- Friends

  function buildFriendsView() {
    var v = {};
    v.code = h('code', { id: 'my-code' }, '...');
    v.nick = textInput({ maxlength: '32', 'aria-label': 'My nickname' });
    v.nickDirty = false;
    v.nick.addEventListener('input', function () { v.nickDirty = true; });
    v.list = h('ul', { class: 'list' });

    var nickForm = h('form', { class: 'row', onsubmit: function (e) {
      e.preventDefault();
      var name = v.nick.value.trim();
      if (!name) { toast('Enter a nickname.', 'error'); return; }
      run(nickForm.querySelector('button'), function () { return call('SetNickname', name); }, 'Nickname saved.').then(function (r) {
        if (r.ok) { v.nickDirty = false; refreshStatus(); }
      });
    } },
      h('div', { class: 'grow' }, field('Your nickname', v.nick, 'Friends see this name. It is only a display name; your friend code identifies you.')),
      h('button', { type: 'submit' }, 'Save nickname'));

    var addCode = textInput({ placeholder: 'lanfolk1...', 'aria-label': 'Friend code' });
    var addNick = textInput({ maxlength: '32' });
    var addForm = h('form', { onsubmit: function (e) {
      e.preventDefault();
      var code = addCode.value.trim();
      if (!code) { toast('Paste your friend\'s code first.', 'error'); return; }
      var nick = addNick.value.trim();
      run(addForm.querySelector('button'), function () { return call('AddFriend', code, nick); }).then(function (r) {
        if (!r.ok) return;
        var f = r.value;
        var name = (f && f.nickname) || nick || 'your friend';
        if (f && f.state === 'active') toast('You and ' + name + ' are now friends.');
        else toast('Added ' + name + '. The friendship becomes active after they add your code too.');
        addCode.value = '';
        addNick.value = '';
        refreshFriends();
      });
    } },
      h('div', { class: 'stack' },
        field('Friend code', addCode, 'Paste the code your friend sent you.'),
        field('Nickname (optional)', addNick, 'How you want to see them in your list.'),
        h('div', null, h('button', { type: 'submit', class: 'primary' }, 'Add friend'))));

    v.root = h('section', { class: 'view', id: 'view-friends', 'aria-labelledby': 'h-friends' },
      h('header', null, h('h1', { id: 'h-friends', tabindex: '-1' }, 'Friends')),
      h('div', { class: 'stack' },
        h('div', { class: 'card' },
          h('h2', null, 'Your friend code'),
          h('p', { class: 'muted' }, 'Send this code to a friend through any messaging app. They add it here, and you add theirs.'),
          h('div', { class: 'code-box' }, v.code, h('button', { type: 'button', class: 'primary', onclick: function () {
            if (state.status && state.status.friend_code) copyText(state.status.friend_code, 'Friend code');
          } }, 'Copy code')),
          h('div', { style: 'margin-top:1rem' }, nickForm)),
        h('div', { class: 'card' },
          h('h2', null, 'Add a friend'),
          h('p', { class: 'muted' }, 'A friendship becomes active only after both of you have added each other\'s code. Until then it shows as pending, and nobody can reach your computer.'),
          addForm),
        h('div', { class: 'card' },
          h('div', { class: 'card-head' }, h('h2', null, 'Your friends'),
            h('button', { type: 'button', onclick: function () { openConnCodeDialog(null); } }, 'Connection code...')),
          v.list)));
    return v;
  }

  function updateFriendsIdentity() {
    var v = views.friends;
    var st = state.status;
    if (!st) return;
    v.code.textContent = st.friend_code || '';
    if (!v.nickDirty && document.activeElement !== v.nick) v.nick.value = st.nickname || '';
  }

  function updateFriendsList() {
    var v = views.friends;
    if (!state.friends.length) {
      v.list.replaceChildren(h('li', null, emptyNote('No friends yet. Add a friend with the code they send you.')));
      return;
    }
    var now = Date.now();
    v.list.replaceChildren.apply(v.list, state.friends.map(function (f) {
      var active = f.state === 'active';
      return h('li', null,
        dot(f.online),
        h('div', { class: 'grow' },
          h('div', { class: 'title' }, f.nickname || 'Unnamed friend', badge(F.friendStateLabel(f.state), active ? 'good' : 'fair')),
          h('small', null, active ? F.friendPresence(f, now) : 'Active once ' + (f.nickname || 'they') + ' has added your friend code too.')),
        h('div', { class: 'actions' },
          h('button', { type: 'button', class: 'small', 'aria-label': 'Connection code for ' + f.nickname, onclick: function () { openConnCodeDialog(f.public_key); } }, 'Connection code...'),
          h('button', { type: 'button', class: 'small danger', 'aria-label': 'Remove ' + f.nickname, onclick: function () { removeFriend(f); } }, 'Remove')));
    }));
  }

  function removeFriend(f) {
    confirmDialog({
      title: 'Remove ' + f.nickname + '?',
      message: [f.nickname + ' will be removed from your friend list.',
        'To be friends again, you both need to add each other\'s code.'],
      confirm: 'Remove friend',
      danger: true,
    }).then(function (yes) {
      if (!yes) return;
      run(null, function () { return call('RemoveFriend', f.public_key); }, f.nickname + ' was removed.').then(function () {
        refreshFriends();
        refreshNetworks();
      });
    });
  }

  function openConnCodeDialog(preselectKey) {
    return openModal(function (close, onClose) {
      var select = h('select', { 'aria-label': 'Friend' },
        state.friends.map(function (f) { return h('option', { value: f.public_key, selected: f.public_key === preselectKey }, f.nickname); }));
      var output = h('div', { hidden: true });
      var timer = null;
      onClose(function () { clearInterval(timer); });
      var createBtn = h('button', { type: 'button', class: 'primary', disabled: !state.friends.length, onclick: function () {
        run(createBtn, function () { return call('CreateConnCode', select.value); }).then(function (r) {
          if (!r.ok) return;
          var res = r.value;
          var expiry = h('p', { class: 'muted' });
          function tick() { expiry.textContent = F.formatExpiry(res.expires, Date.now()); }
          tick();
          clearInterval(timer);
          timer = setInterval(tick, 1000);
          output.hidden = false;
          output.replaceChildren(
            h('div', { class: 'code-box' }, h('code', null, res.code), h('button', { type: 'button', onclick: function () { copyText(res.code, 'Connection code'); } }, 'Copy')),
            expiry,
            h('p', { class: 'muted' }, 'Send this code to your friend over a messaging app. It works once and for a short time only.'));
        });
      } }, 'Create code');
      var pasted = h('textarea', { 'aria-label': 'Connection code from a friend', placeholder: 'Paste a connection code here', spellcheck: 'false' });
      var useBtn = h('button', { type: 'button', class: 'primary', onclick: function () {
        var code = pasted.value.trim();
        if (!code) { toast('Paste a connection code first.', 'error'); return; }
        run(useBtn, function () { return call('UseConnCode', code); }, 'Connection code accepted. Lanfolk is connecting.').then(function (r) {
          if (r.ok) close(true);
        });
      } }, 'Use code');
      return {
        title: 'Connection code',
        body: [
          h('p', { class: 'muted' }, 'If Lanfolk cannot find your friend automatically, exchange a short-lived connection code over a messaging app.'),
          h('section', { class: 'stack', 'aria-label': 'Create a code' },
            h('h3', null, 'Create a code for a friend'),
            state.friends.length ? h('div', { class: 'row' }, h('div', { class: 'grow' }, field('Friend', select)), createBtn) : emptyNote('Add a friend first.'),
            output),
          h('section', { class: 'stack', 'aria-label': 'Use a code', style: 'margin-top:.4rem' },
            h('h3', null, 'Use a code you received'), pasted, h('div', null, useBtn)),
        ],
        actions: [h('button', { type: 'button', onclick: function () { close(false); } }, 'Close')],
      };
    });
  }

  // ---- Networks

  var memberPick = {}; // networkId -> chosen friend key, kept across redraws

  function buildNetworksView() {
    var v = {};
    v.list = h('div', { class: 'stack' });
    var nameInput = textInput({ maxlength: '40', 'aria-label': 'New network name', placeholder: 'For example: Friday LAN' });
    var createForm = h('form', { class: 'row', onsubmit: function (e) {
      e.preventDefault();
      var name = nameInput.value.trim();
      if (!name) { toast('Enter a name for the network.', 'error'); return; }
      run(createForm.querySelector('button'), function () { return call('CreateNetwork', name); }, 'Network "' + name + '" created.').then(function (r) {
        if (r.ok) { nameInput.value = ''; refreshNetworks(); }
      });
    } }, h('div', { class: 'grow' }, field('Network name', nameInput)), h('button', { type: 'submit', class: 'primary' }, 'Create network'));
    v.root = h('section', { class: 'view', id: 'view-networks', 'aria-labelledby': 'h-networks' },
      h('header', null, h('h1', { id: 'h-networks', tabindex: '-1' }, 'Networks'),
        h('p', { class: 'muted' }, 'You can be active in one network at a time. Activating another network switches you over to it. The owner of a network adds and removes members.')),
      h('div', { class: 'stack' },
        h('div', { class: 'card' }, h('h2', null, 'Create a network'), h('p', { class: 'muted' }, 'You become the owner and can add your active friends.'), createForm),
        v.list));
    return v;
  }

  function updateNetworksList() {
    var v = views.networks;
    if (!state.networks.length) {
      v.list.replaceChildren(h('div', { class: 'card' }, emptyNote('You are not in any network yet. Create one above, or ask a friend to add you to theirs.')));
      return;
    }
    v.list.replaceChildren.apply(v.list, state.networks.map(networkCard));
  }

  function networkCard(n) {
    var members = n.members || [];
    var head = h('div', { class: 'card-head' },
      h('div', { class: 'title actions' }, h('h2', null, n.name),
        n.is_owner ? badge('Owner', 'accent') : null,
        n.active ? badge('Active', 'good') : null),
      h('small', null, F.plural(members.length, 'member')));

    var memberList = h('ul', { class: 'list members', 'aria-label': 'Members of ' + n.name }, members.map(function (m) {
      var canRemove = n.is_owner && !m.is_self && !m.is_owner;
      return h('li', null,
        h('div', { class: 'grow' },
          h('div', { class: 'title' }, m.nickname, m.is_self ? badge('You', 'neutral') : null, m.is_owner ? badge('Owner', 'accent') : null),
          h('small', { class: 'ip' }, m.virtual_ip)),
        canRemove ? h('button', { type: 'button', class: 'small danger', 'aria-label': 'Remove ' + m.nickname + ' from ' + n.name, onclick: function () { removeMember(n, m); } }, 'Remove') : null);
    }));

    var parts = [head, memberList];

    if (n.is_owner) {
      var addable = F.addableFriends(state.friends, n);
      var select = h('select', { 'aria-label': 'Friend to add to ' + n.name, disabled: !addable.length, onchange: function () { memberPick[n.id] = select.value; } },
        addable.map(function (f) { return h('option', { value: f.public_key, selected: f.public_key === memberPick[n.id] }, f.nickname); }));
      var addBtn = h('button', { type: 'button', disabled: !addable.length, onclick: function () {
        var fr = addable.find(function (f) { return f.public_key === select.value; });
        run(addBtn, function () { return call('AddMember', n.id, select.value); }, fr ? fr.nickname + ' was added to ' + n.name + '.' : null).then(function (r) {
          if (r.ok) { delete memberPick[n.id]; refreshNetworks(); }
        });
      } }, 'Add member');
      parts.push(h('div', { class: 'row' },
        h('div', { class: 'grow' }, field('Add a friend to this network', select,
          addable.length ? 'Only friends whose friendship is active can be added.' : 'No friends to add. A friend appears here once you have both added each other\'s code.')),
        addBtn));
    }

    var relays = n.relays || [];
    parts.push(h('p', { class: 'muted', style: 'margin:.8rem 0 .2rem' },
      relays.length
        ? [n.is_owner ? 'Your relays for this network: ' : 'Relays chosen by the owner: ', relays.map(F.formatRelay).join('; ')]
        : (n.is_owner ? 'No owner relays set. Members use the default relay and their own.' : null)));

    var actions = [];
    if (n.active) {
      actions.push(h('button', { type: 'button', onclick: function (e) {
        run(e.currentTarget, function () { return call('ActivateNetwork', ''); }, 'Network deactivated.').then(function () { refreshNetworks(); refreshStatus(); });
      } }, 'Deactivate'));
    } else {
      actions.push(h('button', { type: 'button', class: 'primary', onclick: function (e) {
        activateNetwork(e.currentTarget, n);
      } }, 'Activate'));
    }
    if (n.is_owner) {
      actions.push(h('button', { type: 'button', onclick: function () { renameNetwork(n); } }, 'Rename...'));
      actions.push(h('button', { type: 'button', onclick: function () { editRelays(n); } }, 'Relays...'));
    }
    actions.push(h('button', { type: 'button', class: 'danger', onclick: function () { leaveNetwork(n); } }, 'Leave network'));
    parts.push(h('div', { class: 'actions', style: 'margin-top:.8rem' }, actions));

    return h('section', { class: 'net-card' + (n.active ? ' active' : ''), 'aria-label': n.name }, parts);
  }

  function activateNetwork(btn, n) {
    var current = state.networks.find(function (x) { return x.active; });
    var go = current
      ? confirmDialog({
          title: 'Switch to ' + n.name + '?',
          message: 'You can be active in one network at a time. "' + current.name + '" will be deactivated and its members will lose their connection to you.',
          confirm: 'Switch network',
        })
      : Promise.resolve(true);
    go.then(function (yes) {
      if (!yes) return;
      run(btn, function () { return call('ActivateNetwork', n.id); }, '"' + n.name + '" is now your active network.').then(function () {
        refreshNetworks();
        refreshStatus();
      });
    });
  }

  function removeMember(n, m) {
    confirmDialog({
      title: 'Remove ' + m.nickname + '?',
      message: m.nickname + ' will be removed from "' + n.name + '" and loses access right away. You can add them again later.',
      confirm: 'Remove member',
      danger: true,
    }).then(function (yes) {
      if (!yes) return;
      run(null, function () { return call('RemoveMember', n.id, m.public_key); }, m.nickname + ' was removed from ' + n.name + '.').then(refreshNetworks);
    });
  }

  function leaveNetwork(n) {
    confirmDialog({
      title: 'Leave ' + n.name + '?',
      message: n.is_owner
        ? ['You own this network. After you leave you can no longer manage it or add members.', 'This cannot be undone.']
        : ['You will no longer be a member of "' + n.name + '". The owner has to add you again if you want to come back.'],
      confirm: 'Leave network',
      danger: true,
    }).then(function (yes) {
      if (!yes) return;
      run(null, function () { return call('LeaveNetwork', n.id); }, 'You left "' + n.name + '".').then(function () {
        refreshNetworks();
        refreshStatus();
      });
    });
  }

  function renameNetwork(n) {
    openModal(function (close) {
      var input = textInput({ value: n.name, maxlength: '40', 'aria-label': 'Network name' });
      var save = h('button', { type: 'submit', class: 'primary' }, 'Save');
      var form = h('form', { class: 'stack', onsubmit: function (e) {
        e.preventDefault();
        var name = input.value.trim();
        if (!name) { toast('Enter a name for the network.', 'error'); return; }
        run(save, function () { return call('RenameNetwork', n.id, name); }, 'Network renamed.').then(function (r) { if (r.ok) close(true); });
      } }, field('Network name', input), h('div', { class: 'dlg-actions' }, h('button', { type: 'button', onclick: function () { close(false); } }, 'Cancel'), save));
      return { title: 'Rename network', body: form, actions: [], focus: input };
    }).then(function (saved) { if (saved) refreshNetworks(); });
  }

  function editRelays(n) {
    var MAX = 3;
    openModal(function (close) {
      var rows = (n.relays || []).map(function (r) { return { host: r.host, udp: String(r.udp_port), tcp: String(r.tcp_port) }; });
      var list = h('div');
      var addBtn = h('button', { type: 'button', onclick: function () { rows.push({ host: '', udp: '3478', tcp: '443' }); draw(); } }, 'Add relay');
      var err = h('p', { class: 'err', role: 'alert', hidden: true });
      var save = h('button', { type: 'button', class: 'primary', onclick: function () {
        for (var i = 0; i < rows.length; i++) {
          var msg = F.validateRelay(rows[i]);
          if (msg) { err.hidden = false; err.textContent = 'Relay ' + (i + 1) + ': ' + msg; return; }
        }
        err.hidden = true;
        run(save, function () { return call('SetNetworkRelays', n.id, rows.map(F.relayFromForm)); }, 'Relays saved.').then(function (r) { if (r.ok) close(true); });
      } }, 'Save relays');
      function draw() {
        var nodes = rows.length ? rows.map(function (row, i) {
          return h('div', { class: 'relay-edit' },
            field('Host', textInput({ value: row.host, 'aria-label': 'Relay ' + (i + 1) + ' host', placeholder: 'relay.example.net', oninput: function (e) { row.host = e.target.value; } })),
            field('UDP port', textInput({ value: row.udp, inputmode: 'numeric', 'aria-label': 'Relay ' + (i + 1) + ' UDP port', oninput: function (e) { row.udp = e.target.value; } })),
            field('TCP port', textInput({ value: row.tcp, inputmode: 'numeric', 'aria-label': 'Relay ' + (i + 1) + ' TCP port', oninput: function (e) { row.tcp = e.target.value; } })),
            h('button', { type: 'button', class: 'small danger', 'aria-label': 'Remove relay ' + (i + 1), onclick: function () { rows.splice(i, 1); draw(); } }, 'Remove'));
        }) : [emptyNote('No relays. Members use the default relay and their own.')];
        list.replaceChildren.apply(list, nodes);
        addBtn.disabled = rows.length >= MAX;
      }
      draw();
      return {
        title: 'Relays for ' + n.name,
        body: [
          h('p', { class: 'muted' }, 'Relays you add here are signed into the member list, so every member uses them in addition to the default relay. You can add up to ' + MAX + '.'),
          list, h('div', null, addBtn), err,
        ],
        actions: [h('button', { type: 'button', onclick: function () { close(false); } }, 'Cancel'), save],
      };
    }).then(function (saved) { if (saved) refreshNetworks(); });
  }

  // ---- Settings

  function buildSettingsView() {
    var v = {};
    v.opts = h('div');
    v.port = textInput({ inputmode: 'numeric', 'aria-label': 'Manual UDP port', value: '0' });
    v.defaultRelay = h('code', null, '...');
    v.relayList = h('ul', { class: 'list' });
    v.relayHost = textInput({ placeholder: 'relay.example.net', 'aria-label': 'Relay host' });
    v.relayUDP = textInput({ value: '3478', inputmode: 'numeric', 'aria-label': 'Relay UDP port' });
    v.relayTCP = textInput({ value: '443', inputmode: 'numeric', 'aria-label': 'Relay TCP port' });

    var portForm = h('form', { class: 'row', onsubmit: function (e) {
      e.preventDefault();
      var msg = F.validatePort(v.port.value, { allowZero: true });
      if (msg) { toast(msg, 'error'); return; }
      saveSettings({ manual_port: Number(v.port.value.trim()) }, 'Port saved.', portForm.querySelector('button'));
    } }, h('div', { class: 'grow' }, field('Manual UDP port', v.port, '0 lets Lanfolk choose a port automatically. A fixed port helps if you forward it on your router.')),
      h('button', { type: 'submit' }, 'Save port'));

    var relayForm = h('form', { class: 'relay-edit', onsubmit: function (e) {
      e.preventDefault();
      var form = { host: v.relayHost.value, udp: v.relayUDP.value, tcp: v.relayTCP.value };
      var msg = F.validateRelay(form);
      if (msg) { toast(msg, 'error'); return; }
      var relays = (state.settings ? state.settings.custom_relays : []).concat([F.relayFromForm(form)]);
      saveSettings({ custom_relays: relays }, 'Relay added.', relayForm.querySelector('button')).then(function (ok) {
        if (ok) v.relayHost.value = '';
      });
    } }, field('Host', v.relayHost), field('UDP port', v.relayUDP), field('TCP port', v.relayTCP), h('button', { type: 'submit' }, 'Add relay'));

    // Diagnostics
    var diagBtn = h('button', { type: 'button', onclick: function () {
      run(diagBtn, function () { return call('ExportDiagnostics'); }).then(function (r) {
        if (r.ok && r.value) toast('Diagnostics report saved to ' + r.value);
      });
    } }, 'Export diagnostics report...');

    // Identity backup
    var expPw = h('input', { type: 'password', autocomplete: 'new-password', 'aria-label': 'Backup password' });
    var expPw2 = h('input', { type: 'password', autocomplete: 'new-password', 'aria-label': 'Repeat backup password' });
    var expOut = h('div', { hidden: true });
    var expBtn = h('button', { type: 'button', onclick: function () {
      if (expPw.value.length < 8) { toast('Use a password with at least 8 characters.', 'error'); return; }
      if (expPw.value !== expPw2.value) { toast('The two passwords do not match.', 'error'); return; }
      run(expBtn, function () { return call('ExportIdentity', expPw.value); }).then(function (r) {
        if (!r.ok) return;
        var backup = r.value;
        expPw.value = '';
        expPw2.value = '';
        expOut.hidden = false;
        expOut.replaceChildren(
          h('textarea', { readonly: true, 'aria-label': 'Identity backup', onfocus: function (e) { e.target.select(); } }, backup),
          h('div', { class: 'actions' },
            h('button', { type: 'button', onclick: function () { copyText(backup, 'Backup'); } }, 'Copy backup'),
            h('button', { type: 'button', onclick: function (e) {
              run(e.currentTarget, function () { return call('SaveTextFile', 'Save identity backup', 'lanfolk-identity-backup.txt', backup); }).then(function (r2) {
                if (r2.ok && r2.value) toast('Backup saved to ' + r2.value);
              });
            } }, 'Save to file...')),
          h('p', { class: 'muted' }, 'Keep this backup and its password safe. Anyone who has both can act as you.'));
      });
    } }, 'Create backup');

    var impText = h('textarea', { 'aria-label': 'Identity backup to import', placeholder: 'Paste a backup here', spellcheck: 'false' });
    var impPw = h('input', { type: 'password', autocomplete: 'off', 'aria-label': 'Password of the backup' });
    var impBtn = h('button', { type: 'button', class: 'danger', onclick: function () {
      var backup = impText.value.trim();
      if (!backup) { toast('Paste a backup or load it from a file first.', 'error'); return; }
      if (!impPw.value) { toast('Enter the password of the backup.', 'error'); return; }
      confirmDialog({
        title: 'Replace your identity?',
        message: ['This replaces your current identity, including your friend code, with the one in the backup.',
          'Your friends know you by your current friend code. After the import they will need your new code, and networks you own now may be lost.',
          'Create a backup of your current identity first if you might want it back.'],
        confirm: 'Replace my identity',
        danger: true,
      }).then(function (yes) {
        if (!yes) return;
        run(impBtn, function () { return call('ImportIdentity', backup, impPw.value); }, 'Identity imported.').then(function (r) {
          if (!r.ok) return;
          impText.value = '';
          impPw.value = '';
          refreshAll();
        });
      });
    } }, 'Import and replace identity');
    var loadBtn = h('button', { type: 'button', onclick: function () {
      run(loadBtn, function () { return call('OpenTextFile', 'Open identity backup'); }).then(function (r) {
        if (r.ok && r.value) impText.value = r.value;
      });
    } }, 'Load from file...');

    v.root = h('section', { class: 'view', id: 'view-settings', 'aria-labelledby': 'h-settings' },
      h('header', null, h('h1', { id: 'h-settings', tabindex: '-1' }, 'Settings')),
      h('div', { class: 'stack' },
        h('div', { class: 'card' }, h('h2', null, 'Connection'), v.opts, h('div', { style: 'margin-top:.8rem' }, portForm)),
        h('div', { class: 'card' },
          h('h2', null, 'Relays'),
          h('p', { class: 'muted' }, 'Relays forward your encrypted traffic when a direct connection is not possible. Two members can only meet at a relay they both use.'),
          h('div', null, h('small', null, 'Default relay (built in, cannot be changed)'), h('div', null, v.defaultRelay)),
          h('h3', { style: 'margin:1rem 0 .3rem' }, 'Your own relays'),
          v.relayList, h('div', { style: 'margin-top:.6rem' }, relayForm)),
        h('div', { class: 'card' },
          h('h2', null, 'Diagnostics'),
          h('p', { class: 'muted' }, 'Save a report with the service version, connection state and recent events. Attach it when you ask for help. Review it before sharing it publicly.'),
          diagBtn),
        h('div', { class: 'card' },
          h('h2', null, 'Identity backup'),
          h('p', { class: 'muted' }, 'Your identity is created on this computer. A backup lets you move it to another computer or restore it after a reinstall.'),
          h('h3', null, 'Export'),
          h('div', { class: 'row', style: 'margin:.4rem 0' },
            h('div', { class: 'grow' }, field('Password (at least 8 characters)', expPw)),
            h('div', { class: 'grow' }, field('Repeat password', expPw2)),
            expBtn),
          expOut,
          h('h3', { style: 'margin-top:1.2rem' }, 'Import'),
          h('div', { class: 'banner warn', style: 'margin-top:.4rem' }, h('p', null, 'Importing replaces your current identity and friend code. Friends who know your old code will need the new one.')),
          h('div', { class: 'stack' }, impText, h('div', { class: 'row' }, h('div', { class: 'grow' }, field('Password of the backup', impPw)), loadBtn, impBtn)))));
    return v;
  }

  var OPTION_DEFS = [
    { key: 'relay_only', title: 'Relay only', text: 'Hides your IP address from the other members by sending everything through a relay. Your ping may increase.' },
    { key: 'compat_mode', title: 'Compatibility mode', text: 'Helps older games that look for other players on the network. It may affect broadcast traffic on your physical LAN while it is active.' },
    { key: 'port_mapping', title: 'Port mapping (UPnP, NAT-PMP, PCP)', text: 'Asks your router to open a port automatically. This improves direct connections. Lanfolk works without it.' },
    { key: 'dht_fallback', title: 'DHT fallback', text: 'If no relay can be reached, Lanfolk looks for friends through the public BitTorrent DHT. It is used only while no relay is reachable, and it exposes your IP address to DHT nodes.' },
  ];

  function updateSettingsView() {
    var v = views.settings;
    var s = state.settings;
    if (!s) return;
    v.opts.replaceChildren.apply(v.opts, OPTION_DEFS.map(function (d) {
      var input = h('input', { type: 'checkbox', checked: !!s[d.key], onchange: function () {
        var patch = {};
        patch[d.key] = input.checked;
        input.disabled = true;
        saveSettings(patch, d.title + (input.checked ? ' turned on.' : ' turned off.')).then(function (ok) {
          if (!ok) input.checked = !input.checked;
          input.disabled = false;
        });
      } });
      return h('label', { class: 'opt' }, input, h('span', null, h('strong', null, d.title), h('small', null, d.text)));
    }));
    if (document.activeElement !== v.port) v.port.value = String(s.manual_port || 0);
    v.defaultRelay.textContent = s.default_relay || 'None';
    var relays = s.custom_relays || [];
    v.relayList.replaceChildren.apply(v.relayList, relays.length
      ? relays.map(function (r, i) {
          return h('li', null, h('div', { class: 'grow' }, h('span', { class: 'ip' }, F.formatRelay(r))),
            h('button', { type: 'button', class: 'small danger', 'aria-label': 'Remove relay ' + r.host, onclick: function (e) {
              var rest = relays.filter(function (_, j) { return j !== i; });
              saveSettings({ custom_relays: rest }, 'Relay removed.', e.currentTarget);
            } }, 'Remove'));
        })
      : [h('li', null, emptyNote('You have not added any relays.'))]);
  }

  /** Merges patch into the current settings and stores them. Resolves with true on success. */
  function saveSettings(patch, okText, btn) {
    var next = Object.assign({}, state.settings, patch);
    return run(btn, function () { return call('SetSettings', next); }, okText).then(function (r) {
      if (r.ok) state.settings = next;
      return refreshSettings().then(function () { return r.ok; });
    });
  }

  // ---------------------------------------------------------------- status bar

  function renderStatusbar() {
    var bar = $('#statusbar');
    bar.hidden = state.connected === null;
    if (bar.hidden) return;
    var items = [];
    if (state.connected) items.push(h('span', { class: 'item' }, dot(true), 'Service connected'));
    else items.push(h('span', { class: 'item down' }, dot(false), 'Service not reachable'));
    var st = state.status;
    if (state.connected && st) {
      var relays = st.relays || [];
      if (!relays.length) items.push(h('span', { class: 'item slow' }, 'No relay configured'));
      relays.forEach(function (r) {
        var text;
        if (r.mode === 'down') text = 'down';
        else if (r.mode === 'tcp') text = 'TCP fallback, higher latency, ' + F.formatMs(r.rtt_ms);
        else text = 'UDP, ' + F.formatMs(r.rtt_ms);
        items.push(h('span', { class: 'item ' + (r.mode === 'down' ? 'down' : r.mode === 'tcp' ? 'slow' : ''), title: 'Relay (' + r.source + '): ' + r.name + ' - ' + F.relayModeLabel(r.mode) },
          'Relay ' + r.name + ': ' + text));
      });
      items.push(h('span', { class: 'item', title: 'DHT: ' + F.dhtLabel(st.dht) }, 'DHT: ' + (st.dht || 'unknown')));
      if (st.listen_port) {
        items.push(h('span', { class: 'item' }, 'UDP port ' + st.listen_port + (st.port_mapping ? ' (mapped via ' + st.port_mapping + ')' : '')));
      }
      items.push(h('span', { class: 'spacer' }, 'Lanfolk ' + (st.version || '')));
    }
    bar.replaceChildren.apply(bar, items);
  }

  // ------------------------------------------------------------ data refresh

  function refreshStatus() {
    return call('Status').then(function (s) { state.status = s; renderStatus(); }, noop);
  }
  function refreshFriends() {
    return call('ListFriends').then(function (f) { state.friends = f || []; renderFriends(); }, noop);
  }
  function refreshNetworks() {
    return call('ListNetworks').then(function (n) { state.networks = n || []; renderNetworks(); }, noop);
  }
  function refreshPeers() {
    return call('ListPeers').then(function (p) { state.peers = p || []; renderPeers(); }, noop);
  }
  function refreshSettings() {
    return call('GetSettings').then(function (s) { state.settings = s; updateSettingsView(); }, noop);
  }
  function refreshAll() {
    return Promise.all([refreshStatus(), refreshFriends(), refreshNetworks(), refreshPeers(), refreshSettings()]);
  }
  function noop() {}

  function renderStatus() {
    renderStatusbar();
    updateNetworkView();
    updateFriendsIdentity();
  }
  function renderPeers() { updateNetworkView(); }
  function renderFriends() { updateFriendsList(); updateNetworksList(); }
  function renderNetworks() { updateNetworksList(); updateNetworkView(); }

  // ------------------------------------------------------------- navigation

  function buildNav() {
    var list = $('#nav-list');
    VIEWS.forEach(function (view) {
      var btn = h('button', { type: 'button', id: 'nav-' + view.id, onclick: function () { showView(view.id, true); } }, view.label);
      list.appendChild(h('li', null, btn));
    });
  }

  function showView(id, focusHeading) {
    state.view = id;
    VIEWS.forEach(function (view) {
      var btn = $('#nav-' + view.id);
      if (view.id === id) btn.setAttribute('aria-current', 'page');
      else btn.removeAttribute('aria-current');
      views[view.id].root.hidden = view.id !== id;
    });
    if (id === 'settings') refreshSettings();
    if (id === 'friends') refreshFriends();
    if (id === 'networks') { refreshNetworks(); refreshFriends(); }
    if (focusHeading) {
      var head = views[id].root.querySelector('h1');
      if (head) head.focus();
    }
  }

  // ------------------------------------------------------------ service state

  function applyService(s) {
    var was = state.connected;
    state.connected = !!s.connected;
    state.message = s.message || '';
    $('#offline').hidden = state.connected;
    $('#shell').hidden = !state.connected;
    $('#offline-detail').textContent = state.message;
    if (state.connected && was !== true) refreshAll();
    renderStatusbar();
  }

  function setupRetry() {
    var btn = $('#retry');
    btn.addEventListener('click', function () {
      btn.disabled = true;
      call('Retry').catch(noop).then(function () { return call('Connected'); }).then(function (ok) {
        if (ok) applyService({ connected: true });
        else $('#offline-detail').textContent = 'Still not reachable. Make sure the Lanfolk service is running.';
      }, noop).then(function () {
        setTimeout(function () { btn.disabled = false; }, 800);
      });
    });
  }

  function setupEvents() {
    var on = window.runtime.EventsOn;
    on('service', function (d) { applyService(d || {}); });
    on('status', function (d) { state.status = d; renderStatus(); });
    on('peers', function (d) { state.peers = d || []; renderPeers(); });
    on('friends', function (d) { state.friends = d || []; renderFriends(); });
    on('networks', function (d) { state.networks = d || []; renderNetworks(); });
  }

  // -------------------------------------------------------------------- start

  function waitForBackend(tries) {
    return new Promise(function (resolve, reject) {
      (function check(n) {
        if (backend() && window.runtime && window.runtime.EventsOn) return resolve();
        if (n <= 0) return reject(new Error('The Lanfolk app could not start. Please restart it.'));
        setTimeout(function () { check(n - 1); }, 50);
      })(tries);
    });
  }

  function init() {
    buildNav();
    views.network = buildNetworkView();
    views.friends = buildFriendsView();
    views.networks = buildNetworksView();
    views.settings = buildSettingsView();
    var main = $('#main');
    VIEWS.forEach(function (view) {
      views[view.id].root.hidden = true;
      main.appendChild(views[view.id].root);
    });
    showView('network');
    setupRetry();
    updateNetworkView();
    updateFriendsList();
    updateNetworksList();

    waitForBackend(200).then(function () {
      setupEvents();
      return call('Connected');
    }).then(function (ok) {
      applyService({ connected: ok });
    }).catch(function (e) {
      $('#offline-title').textContent = 'Lanfolk could not start';
      $('#offline-detail').textContent = errText(e);
      $('#offline').hidden = false;
    });

    // Keep "last seen 5 min ago" fresh.
    setInterval(function () { if (state.view === 'friends') updateFriendsList(); }, 30000);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
