// Icons: 16 x 16 outline glyphs on a 1.5 px stroke, drawn for Lanfolk.
// icon(name) returns an inline SVG string that inherits currentColor.
(function (root) {
  'use strict';
  var P = {
    settings:
      '<path d="M6.9 1.8h2.2l.4 1.7c.4.2.8.4 1.1.7l1.7-.5 1.1 1.9-1.3 1.2c.1.4.1.9 0 1.3l1.3 1.2-1.1 1.9-1.7-.5c-.3.3-.7.5-1.1.7l-.4 1.7H6.9l-.4-1.7c-.4-.2-.8-.4-1.1-.7l-1.7.5-1.1-1.9 1.3-1.2a3.2 3.2 0 0 1 0-1.3L2.6 5.6l1.1-1.9 1.7.5c.3-.3.7-.5 1.1-.7z"/><circle cx="8" cy="8" r="2"/>',
    minimize: '<path d="M3.5 8h9"/>',
    close: '<path d="M4 4l8 8M12 4l-8 8"/>',
    chevron: '<path d="M6 3.5L10.5 8 6 12.5"/>',
    back: '<path d="M13 8H3.5M7.5 4L3.5 8l4 4"/>',
    copy: '<rect x="5.5" y="5.5" width="8" height="8" rx="1.5"/><path d="M10.5 5.5V3.8c0-.7-.6-1.3-1.3-1.3H3.8c-.7 0-1.3.6-1.3 1.3v5.4c0 .7.6 1.3 1.3 1.3h1.7"/>',
    check: '<path d="M3 8.5l3.2 3L13 4.5"/>',
    plus: '<path d="M8 3v10M3 8h10"/>',
    join: '<path d="M9.5 2.5h2.7c.7 0 1.3.6 1.3 1.3v8.4c0 .7-.6 1.3-1.3 1.3H9.5"/><path d="M2.5 8h7M6.5 5l3 3-3 3"/>',
    more: '<circle cx="3.5" cy="8" r=".6"/><circle cx="8" cy="8" r=".6"/><circle cx="12.5" cy="8" r=".6"/>',
    power: '<path d="M8 2v5.5"/><path d="M4.9 4.1a5 5 0 1 0 6.2 0"/>',
    warning: '<path d="M8 2.5l6 10.5H2z"/><path d="M8 6.5v3M8 11.3v.2"/>',
    invite: '<circle cx="6" cy="5.5" r="2.5"/><path d="M1.8 13.5c.5-2.3 2.2-3.5 4.2-3.5s3.7 1.2 4.2 3.5M12.5 5.5v4M10.5 7.5h4"/>',
    refresh: '<path d="M13.2 8A5.2 5.2 0 1 1 11.6 4.2"/><path d="M12.5 1.8v2.8H9.7"/>',
    external: '<path d="M9 2.5h4.5V7M13.5 2.5L7.5 8.5M11.5 9.5v3c0 .6-.4 1-1 1h-7c-.6 0-1-.4-1-1v-7c0-.6.4-1 1-1h3"/>',
    paste: '<rect x="3" y="3" width="10" height="11" rx="1.5"/><path d="M6 3V2.3c0-.4.3-.8.8-.8h2.4c.5 0 .8.4.8.8V3M5.5 7.5h5M5.5 10h3.5"/>',
    trash: '<path d="M2.5 4.5h11M6.5 2.5h3M4 4.5l.7 8.3c0 .4.4.7.8.7h5c.4 0 .8-.3.8-.7L12 4.5"/>',
    edit: '<path d="M10.5 2.8l2.7 2.7-7.6 7.6-3.1.4.4-3.1z"/>',
    info: '<circle cx="8" cy="8" r="5.75"/><path d="M8 7.2v3.6M8 5.1v.2"/>',
    leave: '<path d="M6.5 2.5H3.8c-.7 0-1.3.6-1.3 1.3v8.4c0 .7.6 1.3 1.3 1.3h2.7"/><path d="M13.5 8h-7M10.5 5l3 3-3 3"/>',
  };
  function icon(name, cls) {
    var body = P[name] || '';
    return (
      '<svg class="ic' + (cls ? ' ' + cls : '') + '" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false" ' +
      'fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">' + body + '</svg>'
    );
  }
  // The brand mark (same geometry as assets/brand/mark.svg), inline so it
  // can follow the theme with CSS variables.
  function mark(size) {
    var s = size || 20;
    return (
      '<svg class="mark" viewBox="0 0 48 48" width="' + s + '" height="' + s + '" aria-hidden="true">' +
      '<g stroke="var(--mark-a)" stroke-width="4.5" stroke-linecap="round">' +
      '<line x1="30.505" y1="15.005" x2="32.995" y2="17.495"/><line x1="32.995" y1="30.505" x2="30.505" y2="32.995"/>' +
      '<line x1="17.495" y1="32.995" x2="15.005" y2="30.505"/></g>' +
      '<g fill="var(--mark-a)"><circle cx="24" cy="8.5" r="6"/><circle cx="39.5" cy="24" r="6"/><circle cx="24" cy="39.5" r="6"/></g>' +
      '<circle cx="8.5" cy="24" r="6" fill="var(--mark-b)"/></svg>'
    );
  }
  root.icon = icon;
  root.mark = mark;
})(typeof self !== 'undefined' ? self : this);
