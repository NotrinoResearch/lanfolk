/* Pure formatting helpers, shared by the app and its Node tests.
 * No DOM access here. Loaded as a classic script (window.LanfolkFormat)
 * or with require() in Node. */
(function (root, factory) {
  'use strict';
  var api = factory();
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  } else {
    root.LanfolkFormat = api;
  }
})(typeof self !== 'undefined' ? self : this, function () {
  'use strict';

  var DASH = '—';

  function isNum(v) {
    return typeof v === 'number' && isFinite(v);
  }

  function plural(n, word) {
    return n + ' ' + word + (n === 1 ? '' : 's');
  }

  /** Milliseconds: "3.5 ms", "42 ms"; a dash when there is no value. */
  function formatMs(v) {
    if (!isNum(v) || v < 0) return DASH;
    var tenth = Math.round(v * 10) / 10;
    if (tenth < 10) {
      return (Number.isInteger(tenth) ? String(tenth) : tenth.toFixed(1)) + ' ms';
    }
    return Math.round(v) + ' ms';
  }

  /** Percent: "0%", "0.5%", "2.3%", "12%". */
  function formatPercent(v) {
    if (!isNum(v) || v < 0) return DASH;
    if (v >= 10) return Math.round(v) + '%';
    var tenth = Math.round(v * 10) / 10;
    return (Number.isInteger(tenth) ? String(tenth) : tenth.toFixed(1)) + '%';
  }

  var QUALITY = {
    good: { label: 'Good', cls: 'good' },
    fair: { label: 'Fair', cls: 'fair' },
    poor: { label: 'Poor', cls: 'poor' },
  };

  /** Quality badge text and CSS class. */
  function qualityBadge(q) {
    var b = QUALITY[String(q || '').toLowerCase()];
    return b ? { label: b.label, cls: b.cls } : { label: 'Unknown', cls: 'unknown' };
  }

  var PATHS = {
    direct: 'Direct',
    'member-relay': 'Member relay',
    'shared-relay': 'Shared relay',
    'tcp-fallback': 'TCP fallback',
  };

  /** Connection path type shown to the user. */
  function pathLabel(path) {
    return PATHS[path] || 'Not connected';
  }

  /** "via Mehmet" / "via relay:3478"; empty for direct paths. */
  function relayDetail(peer) {
    var d = peer && peer.relay_detail;
    if (!d) return '';
    if (peer.path === 'member-relay' || peer.path === 'shared-relay') return 'via ' + d;
    if (peer.path === 'tcp-fallback') return 'via ' + d + ' (higher latency)';
    return '';
  }

  /** A shared relay's transport as shown in the status bar. */
  function relayModeLabel(mode) {
    switch (mode) {
      case 'udp':
        return 'UDP';
      case 'tcp':
        return 'TCP fallback (higher latency)';
      case 'down':
        return 'Down';
      default:
        return 'Unknown';
    }
  }

  function dhtLabel(state) {
    switch (state) {
      case 'off':
        return 'Off';
      case 'standby':
        return 'Standby (starts only if no relay is reachable)';
      case 'running':
        return 'Running';
      default:
        return 'Unknown';
    }
  }

  /** Go encodes a missing time as year 1. */
  function isZeroTime(iso) {
    if (!iso) return true;
    var t = Date.parse(iso);
    return !isFinite(t) || new Date(t).getUTCFullYear() < 1970;
  }

  /** "Online now", "Last seen 5 min ago", "Never seen". */
  function friendPresence(friend, now) {
    if (friend.online) return 'Online now';
    if (isZeroTime(friend.last_seen_at)) return 'Never seen';
    var secs = Math.floor((now - Date.parse(friend.last_seen_at)) / 1000);
    if (secs < 60) return 'Last seen just now';
    var mins = Math.floor(secs / 60);
    if (mins < 60) return 'Last seen ' + mins + ' min ago';
    var hours = Math.floor(mins / 60);
    if (hours < 24) return 'Last seen ' + hours + ' h ago';
    var days = Math.floor(hours / 24);
    return 'Last seen ' + plural(days, 'day') + ' ago';
  }

  function friendStateLabel(state) {
    return state === 'active' ? 'Active' : 'Pending';
  }

  /** Time left for a connection code. */
  function formatExpiry(iso, now) {
    var t = Date.parse(iso);
    if (!isFinite(t)) return '';
    var left = Math.ceil((t - now) / 1000);
    if (left <= 0) return 'Expired';
    var m = Math.floor(left / 60);
    var s = left % 60;
    if (m > 0 && s > 0) return 'Expires in ' + m + ' min ' + s + ' s';
    if (m > 0) return 'Expires in ' + m + ' min';
    return 'Expires in ' + s + ' s';
  }

  function formatRelay(r) {
    return r.host + ' (UDP ' + r.udp_port + ', TCP ' + r.tcp_port + ')';
  }

  /** Returns an error text, or '' when the port is valid. */
  function validatePort(text, opts) {
    var min = opts && opts.allowZero ? 0 : 1;
    var s = String(text == null ? '' : text).trim();
    var n = Number(s);
    if (!/^\d+$/.test(s) || n < min || n > 65535) {
      return 'Enter a port between ' + min + ' and 65535.';
    }
    return '';
  }

  /** form: {host, udp, tcp} as typed. Returns an error text, or ''. */
  function validateRelay(form) {
    var host = String(form.host || '').trim();
    if (!host) return 'Enter the relay host name or IP address.';
    if (/\s/.test(host)) return 'The host name cannot contain spaces.';
    var e = validatePort(form.udp);
    if (e) return 'UDP port: ' + e;
    e = validatePort(form.tcp);
    if (e) return 'TCP port: ' + e;
    return '';
  }

  function relayFromForm(form) {
    return {
      host: String(form.host).trim(),
      udp_port: Number(String(form.udp).trim()),
      tcp_port: Number(String(form.tcp).trim()),
    };
  }

  /** Online members first, then by nickname. */
  function sortPeers(peers) {
    return peers.slice().sort(function (a, b) {
      if (!!a.online !== !!b.online) return a.online ? -1 : 1;
      return String(a.nickname).localeCompare(String(b.nickname), undefined, { sensitivity: 'base' });
    });
  }

  /** Ping, jitter and loss only mean something for a connected member. */
  function isPeerMeasured(peer) {
    return !!peer.online && !!peer.path && peer.path !== 'none';
  }

  /** Active friends who are not yet members of the network. */
  function addableFriends(friends, network) {
    var members = {};
    (network.members || []).forEach(function (m) {
      members[m.public_key] = true;
    });
    return friends.filter(function (f) {
      return f.state === 'active' && !members[f.public_key];
    });
  }

  /** True when text looks like an invite code (lanfolk-i1...). */
  function looksLikeInvite(text) {
    var s = String(text == null ? '' : text).trim();
    return /^lanfolk-i1[a-z0-9]{20,}$/i.test(s);
  }

  /** "Works for 7 more days" for an invite's expiry time. */
  function inviteValidity(iso, now) {
    var t = Date.parse(iso);
    if (!isFinite(t)) return '';
    var secs = (t - now) / 1000;
    if (secs <= 0) return 'Expired';
    var hours = Math.floor(secs / 3600);
    if (hours < 1) return 'Works for less than an hour';
    if (hours < 24) return 'Works for ' + hours + (hours === 1 ? ' more hour' : ' more hours');
    // Round so a fresh 7-day code reads "7 more days", not 6.
    var days = Math.max(1, Math.round(hours / 24));
    return 'Works for ' + days + (days === 1 ? ' more day' : ' more days');
  }

  var SHORT_PATHS = {
    direct: 'Direct',
    'shared-relay': 'Relay',
    'tcp-fallback': 'Relay (TCP)',
  };

  /** What a member row shows on the right: latency and how we reach them. */
  function memberSummary(peer) {
    if (!peer || !peer.online) return { text: 'Offline', sub: '', state: 'offline' };
    if (!peer.path || peer.path === 'none') return { text: 'Connecting…', sub: '', state: 'connecting' };
    var sub = peer.path === 'member-relay' ? (peer.relay_detail ? 'Via ' + peer.relay_detail : 'Via a member') : SHORT_PATHS[peer.path] || '';
    return { text: formatMs(peer.ping_ms), sub: sub, state: 'online' };
  }

  /** Progress text of an invite the user redeemed. */
  function joinStateText(join) {
    switch (join && join.state) {
      case 'waiting':
        return 'Waiting for ' + (join.owner_nickname || 'the owner') + ' to come online…';
      case 'joining':
        return 'Joining…';
      case 'joined':
        return 'Joined';
      case 'expired':
        return 'The invite expired';
      default:
        return '';
    }
  }

  /** Members other than you, and how many of them are online. */
  function onlineCount(network, peers) {
    var online = {};
    (peers || []).forEach(function (p) {
      if (p.online) online[p.public_key] = true;
    });
    var others = 0;
    var on = 0;
    ((network && network.members) || []).forEach(function (m) {
      if (m.is_self) return;
      others++;
      if (online[m.public_key]) on++;
    });
    return { online: on, others: others };
  }

  /** Suggested name for a new network. */
  function defaultNetworkName(nick) {
    var n = String(nick || '').trim();
    if (!n) return 'My network';
    var suffix = "'s network";
    return n.slice(0, 32 - suffix.length) + suffix;
  }

  return {
    looksLikeInvite: looksLikeInvite,
    inviteValidity: inviteValidity,
    memberSummary: memberSummary,
    joinStateText: joinStateText,
    onlineCount: onlineCount,
    defaultNetworkName: defaultNetworkName,
    plural: plural,
    formatMs: formatMs,
    formatPercent: formatPercent,
    qualityBadge: qualityBadge,
    pathLabel: pathLabel,
    relayDetail: relayDetail,
    relayModeLabel: relayModeLabel,
    dhtLabel: dhtLabel,
    isZeroTime: isZeroTime,
    friendPresence: friendPresence,
    friendStateLabel: friendStateLabel,
    formatExpiry: formatExpiry,
    formatRelay: formatRelay,
    validatePort: validatePort,
    validateRelay: validateRelay,
    relayFromForm: relayFromForm,
    sortPeers: sortPeers,
    isPeerMeasured: isPeerMeasured,
    addableFriends: addableFriends,
  };
});
