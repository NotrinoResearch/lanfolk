---
name: Lanfolk
description: A calm Windows 11 utility that puts friends on one local network.
colors:
  bg: "#f3f3f3"
  surface: "#ffffff"
  surface-2: "#f9f9f9"
  text: "#1b1b1f"
  text-2: "#5c5f66"
  text-3: "#63676e"
  accent: "#2f5bea"
  accent-hover: "#2a52d6"
  accent-press: "#2549c2"
  on-accent: "#ffffff"
  accent-text: "#2449c9"
  ok: "#0c7d55"
  ok-dot: "#14c38e"
  warn: "#8a5a00"
  warn-dot: "#e3a008"
  bad: "#b42318"
  bad-dot: "#e5484d"
  off-dot: "#a3a7ae"
  dark-bg: "#202020"
  dark-surface: "#2b2b2b"
  dark-text: "#f3f3f3"
  dark-accent: "#7b97ff"
  dark-on-accent: "#0b1330"
  dark-ok-dot: "#3ddc97"
  mark-a: "#2f5bea"
  mark-b: "#14c38e"
typography:
  body:
    fontFamily: "Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "14px"
    fontWeight: 400
    lineHeight: "20px"
  caption:
    fontFamily: "Segoe UI Variable Text, Segoe UI, system-ui, sans-serif"
    fontSize: "12px"
    fontWeight: 400
    lineHeight: "16px"
  subtitle:
    fontFamily: "Segoe UI Variable Display, Segoe UI, system-ui, sans-serif"
    fontSize: "20px"
    fontWeight: 600
    lineHeight: "28px"
    letterSpacing: "-0.01em"
  title:
    fontFamily: "Segoe UI Variable Display, Segoe UI, system-ui, sans-serif"
    fontSize: "26px"
    fontWeight: 600
    lineHeight: "34px"
    letterSpacing: "-0.015em"
  code:
    fontFamily: "Cascadia Mono, Consolas, ui-monospace, monospace"
    fontSize: "12.5px"
    lineHeight: "18px"
rounded:
  sm: "5px"
  md: "8px"
  pill: "999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "24px"
components:
  button:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    height: "32px"
    padding: "0 14px"
  button-primary:
    backgroundColor: "{colors.accent}"
    textColor: "{colors.on-accent}"
    rounded: "{rounded.sm}"
    height: "32px"
  button-primary-hover:
    backgroundColor: "{colors.accent-hover}"
  card:
    backgroundColor: "{colors.surface}"
    rounded: "{rounded.md}"
  input:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.text}"
    rounded: "{rounded.sm}"
    height: "32px"
  row:
    height: "40px"
    rounded: "{rounded.sm}"
  titlebar:
    height: "40px"
  caption-button:
    width: "46px"
---

# Lanfolk design system

## Overview

Lanfolk is a calm, trustworthy Windows utility: it works in the background and
is opened briefly to see who is online or why someone lags. The visual world is
the category standard played straight, a classic Windows 11 app (bar: Hamachi's
simplicity, Tailscale for Windows, the Windows 11 Settings app). The interface
shows state, not machinery: "Online in Friday LAN", "18 ms · Direct", before
relay names and ports. Two windows:

- **Main window** (380 x 640, frameless with native shadow and snap, closes to
  the tray): identity card, Networks | Friends tabs, live member rows, Create /
  Join bar.
- **Settings window** (780 x 560, its own process): Windows 11 Settings layout
  with a category sidebar and a content pane.

The tray icon carries the state when the window is closed.

## Colors

Restrained: neutral Mica-like grounds (`bg` #f3f3f3 light, #202020 dark),
white or #2b2b2b cards with hairline borders, one accent.

- **Cobalt** (`accent`) is for primary actions, toggles, selection and the
  mark. Never for decoration.
- **Signal green** (`ok-dot`) means online, and the "you" node in the mark.
  Nothing else is green.
- **Amber** ring (`warn-dot`) means connecting or pending; **red**
  (`bad-dot`) means a problem.
- State is never colour alone: every dot has a text next to it.
- Tertiary text (`text-3`) is kept at 4.5:1 or more on `bg`.

## Typography

One family, the Windows system face: Segoe UI Variable (Text for UI, Display
for titles). A tight scale: 12 caption, 13 secondary, 14 body, 15 the
nickname, 20 view titles, 26 the onboarding and Settings page titles. Numbers
(IPs, ping, ports) use tabular figures. Codes (invite, friend code) use
Cascadia Mono because they are data the user copies.

## Layout

The main window is a single column: a 40 px title bar, a 12 px gutter, the
identity card, the tabs, a scrolling list and a fixed action bar at the
bottom. Sub-views (create, join, invite, add friend) replace the content with
a back arrow and a title, and pin their one primary button to the bottom.
The Settings window is a 220 px sidebar plus a pane capped at 720 px.

## Elevation & Depth

Elevation is declared once per element: cards and inputs use a hairline
border, and only floating layers (menus, the dialog, toasts) have a shadow
(`0 8px 24px` plus `0 2px 6px`, deeper in dark mode). No glass, gradients or
grain.

## Shapes

5 px radius for controls and rows, 8 px for cards, menus and the dialog,
circles for the power button and status dots. The mark is pure geometry:
four nodes on a diamond, three links that stop short of the nodes.

## Components

- **Identity card**: power button (cobalt when online), nickname, status dot
  and text, copyable IP chip.
- **Network group**: chevron, name, online count that includes you, Connect
  (inactive networks) and a ⋯ menu.
- **Member and friend rows**: dot, name with "You · Owner" tags, IP or
  presence below, latency and path on the right. Selecting a row extends the
  same row downward with a two-column readout; it is never a separate card
  and holds no buttons. Actions live in the row menu (⋯, right click,
  Shift+F10).
- **Tabs**: text tabs with a 3 px cobalt underline on the selected one.
- **Settings sidebar**: Windows 11 NavigationView items with the native
  selection pill (3 x 16 px, rounded). This pattern is the brief's canon, not
  a decorative side stripe.
- **Toggle**: Fluent switch with an On/Off label.
- **Dialog**: only for destructive or password confirmations.

## Do's and Don'ts

- Do keep the main screen to state and the two actions a player needs.
- Do put detailed configuration in the Settings window, not in the main one.
- Do use menus for actions on a row; keep rows informational.
- Don't add cards inside cards, coloured side borders on cards, gradients,
  emoji or display fonts.
- Don't show relay names, ports or keys on the main screen.
- Don't use colour as the only signal of state.
