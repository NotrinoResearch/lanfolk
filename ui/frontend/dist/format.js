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

  // One vocabulary everywhere: the row says it short ("Relay"), the details
  // say it in full ("Relay server").
  var PATHS = {
    direct: 'Direct',
    'member-relay': 'Via a member',
    'shared-relay': 'Relay server',
    'tcp-fallback': 'Relay server over TCP',
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

  /**
   * One sentence on what a connection's numbers mean and what helps, or ''
   * when all is well. Plain words first (PRODUCT.md: state, not machinery).
   */
  function connectionAdvice(peer) {
    if (!peer || !peer.online || !peer.path || peer.path === 'none') return '';
    var q = String(peer.quality || '');
    var relayed = peer.path !== 'direct';
    if (q !== 'fair' && q !== 'poor') {
      return relayed ? 'Works well through a relay. A direct connection would be a little faster.' : '';
    }
    if (isNum(peer.loss_pct) && peer.loss_pct > 2) {
      return 'Packets are being lost. Wi-Fi or a busy connection on either side is the usual cause; a cable helps.';
    }
    if (isNum(peer.jitter_ms) && peer.jitter_ms > 30) {
      return 'The delay jumps around. Downloads or streams on either connection are the usual cause.';
    }
    if (relayed) {
      return 'Going through a relay adds delay. Turning on Automatic port mapping (Settings, Connection) on both computers often allows a direct connection.';
    }
    return 'The delay comes from the distance or the internet connections themselves; Lanfolk adds almost nothing.';
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

  var INVITE_RE = /lanfolk-i1[a-z0-9]{20,}/i;

  /** True when text is an invite code (lanfolk-i1...) and nothing else. */
  function looksLikeInvite(text) {
    var s = String(text == null ? '' : text).trim();
    return new RegExp('^' + INVITE_RE.source + '$', 'i').test(s);
  }

  /**
   * The invite code inside a pasted text (a whole chat message with the
   * download link, for example), or '' when there is none.
   */
  function extractInvite(text) {
    var m = String(text == null ? '' : text).match(INVITE_RE);
    return m ? m[0] : '';
  }

  /** The text "Copy invite" puts on the clipboard: ready to send in a chat. */
  function inviteMessage(code, networkName, downloadURL) {
    return 'Join my Lanfolk network "' + networkName + '".\n' +
      'No Lanfolk yet? Install it from ' + downloadURL + '\n' +
      'Then choose Join network and paste this whole message:\n' + code;
  }

  function parseVersion(v) {
    var m = /^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+.*)?$/.exec(String(v == null ? '' : v).trim());
    if (!m) return null;
    return { n: [+m[1], +m[2], +m[3]], pre: m[4] || '' };
  }

  /**
   * Compares release versions ("0.3.0", "v0.3.1-rc.1"): -1, 0 or 1. An
   * unparsable or empty version sorts first (releases before 0.3.0 send none).
   */
  function compareVersions(a, b) {
    var pa = parseVersion(a);
    var pb = parseVersion(b);
    if (!pa || !pb) return !pa && !pb ? 0 : !pa ? -1 : 1;
    for (var i = 0; i < 3; i++) {
      if (pa.n[i] !== pb.n[i]) return pa.n[i] < pb.n[i] ? -1 : 1;
    }
    if (pa.pre === pb.pre) return 0;
    if (!pa.pre) return 1;
    if (!pb.pre) return -1;
    // SemVer 2.0: dot-separated identifiers, numbers numerically and first.
    var ia = pa.pre.split('.');
    var ib = pb.pre.split('.');
    for (var j = 0; j < ia.length && j < ib.length; j++) {
      if (ia[j] === ib[j]) continue;
      var na = /^\d+$/.test(ia[j]);
      var nb = /^\d+$/.test(ib[j]);
      if (na && nb) return +ia[j] < +ib[j] ? -1 : 1;
      if (na !== nb) return na ? -1 : 1;
      return ia[j] < ib[j] ? -1 : 1;
    }
    return ia.length === ib.length ? 0 : ia.length < ib.length ? -1 : 1;
  }

  /**
   * How a member's Lanfolk compares with ours: 'older', 'newer' or ''.
   * A member that sends no version runs a release before 0.3.0. A dev build
   * compares with nothing.
   */
  function versionRelation(theirs, ours) {
    if (!parseVersion(ours) || String(ours).indexOf('dev') >= 0) return '';
    var c = compareVersions(theirs, ours);
    return c < 0 ? 'older' : c > 0 ? 'newer' : '';
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

  /**
   * What a member row shows on the right: the average ping (steadier than
   * the latest), how we reach them and, when it is not good, the quality.
   * state is the dot: online, fair, poor, connecting or offline.
   */
  function memberSummary(peer) {
    if (!peer || !peer.online) return { text: 'Offline', sub: '', state: 'offline' };
    if (!peer.path || peer.path === 'none') return { text: 'Connecting…', sub: '', state: 'connecting' };
    var sub = peer.path === 'member-relay' ? (peer.relay_detail ? 'Via ' + peer.relay_detail : 'Via a member') : SHORT_PATHS[peer.path] || '';
    var q = String(peer.quality || '');
    var state = q === 'fair' || q === 'poor' ? q : 'online';
    if (state !== 'online') sub += ' · ' + QUALITY[q].label;
    var ms = isNum(peer.avg_ping_ms) && peer.avg_ping_ms > 0 ? peer.avg_ping_ms : peer.ping_ms;
    return { text: formatMs(ms), sub: sub, state: state };
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
        return 'The invite expired. Ask for a new one.';
      default:
        return '';
    }
  }

  /** How many members are online, you included when selfOnline. */
  function onlineCount(network, peers, selfOnline) {
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
    return { online: on + (selfOnline ? 1 : 0), total: others + 1, others: others };
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
    extractInvite: extractInvite,
    inviteMessage: inviteMessage,
    compareVersions: compareVersions,
    versionRelation: versionRelation,
    connectionAdvice: connectionAdvice,
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
    formatExpiry: formatExpiry,
    formatRelay: formatRelay,
    validatePort: validatePort,
    validateRelay: validateRelay,
    relayFromForm: relayFromForm,
    isPeerMeasured: isPeerMeasured,
    addableFriends: addableFriends,
  };
});
