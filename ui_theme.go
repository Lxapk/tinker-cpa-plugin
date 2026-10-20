package main

// uiCSS is the panel stylesheet.
//
// It reproduces the reference plugin's look, which is itself taken from CPA's own
// management panel: a warm neutral scale where the action colour is a quiet grey,
// green means good, and both warning and error are the same brick red. Matching
// the host matters — a hand-rolled palette made the panel look like a separate
// app embedded in the frame.
//
// Theme handling is the part that is easy to get wrong. CPA writes
// data-theme="dark" or data-theme="white" on its own root and *removes* the
// attribute when the operator chose "follow system". So the rules are:
//
//	:root, :root[data-theme="dark"]        dark tokens (the default)
//	:root[data-theme="white"|"light"]      explicit light
//	@media (prefers-color-scheme: light)    :root:not([data-theme]) follows the OS
//
// That is why the media query targets only the attribute-less case.
const uiCSS = `
/* ======================= tokens ======================= */
:root,
:root[data-theme="dark"] {
  color-scheme: dark;
  --bg-secondary: #151412;   /* page background */
  --bg-primary: #1d1b18;     /* card surface */
  --bg-tertiary: #262320;    /* hover / secondary */
  --bg-hover: #2e2a26;
  --bg-quinary: #191714;

  --text-primary: #f6f4f1;
  --text-secondary: #c9c3bb;
  --text-tertiary: #9c958d;
  --text-quaternary: #6f6962;

  --border-color: #3a3530;
  --border-primary: #4a453f;
  --border-hover: #5a544d;

  /* Action colour: a neutral grey, not a brand hue. Buttons in this host are quiet;
     the colour that carries meaning is the status one. */
  --primary-color: #8b8680;
  --primary-hover: #9a948e;
  --primary-active: #a6a099;
  --primary-contrast: #ffffff;

  --success-color: #10b981;
  --warning-color: #c65746;
  --error-color: #c65746;
  --quota-medium-color: #ffd862;

  --shadow: 0 1px 3px 0 rgb(0 0 0 / 0.3);
  --shadow-lg: 0 14px 30px rgba(0, 0, 0, 0.4);

  --radius-sm: 4px;
  --radius-md: 8px;
  --radius-lg: 12px;
  --radius-card: 14px;
  --radius-full: 9999px;

  --space-xs: 4px;
  --space-sm: 8px;
  --space-md: 16px;
  --space-lg: 24px;
  --space-xl: 32px;

  --mono: ui-monospace, "SF Mono", "Cascadia Mono", "JetBrains Mono", Menlo, Consolas, monospace;
  --sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC",
          "Microsoft YaHei", "Helvetica Neue", sans-serif;
}

/* Explicit light choice from the host's theme switcher: pure white throughout. */
:root[data-theme="white"],
:root[data-theme="light"] {
  color-scheme: light;
  --bg-secondary: #ffffff;
  --bg-primary: #ffffff;
  --bg-tertiary: #f6f6f6;
  --bg-hover: #f0f0f0;
  --bg-quinary: #ffffff;

  --text-primary: #2d2a26;
  --text-secondary: #6d6760;
  --text-tertiary: #a29c95;
  --text-quaternary: #c0bab3;

  --border-color: #e5e5e5;
  --border-primary: #d9d9d9;
  --border-hover: #cccccc;

  --primary-color: #8b8680;
  --primary-hover: #7f7a74;
  --primary-active: #726d67;
  --primary-contrast: #ffffff;

  --success-color: #10b981;
  --warning-color: #c65746;
  --error-color: #c65746;
  --quota-medium-color: #e0aa14;

  --shadow: 0 1px 2px 0 rgb(0 0 0 / 0.08);
  --shadow-lg: 0 10px 18px -3px rgb(0 0 0 / 0.1);
}

/* "Follow system": the host removes data-theme, so the OS preference decides.
   Paper-toned, matching the host's own :root light palette rather than #fff. */
@media (prefers-color-scheme: light) {
  :root:not([data-theme]) {
    color-scheme: light;
    --bg-secondary: #faf9f5;
    --bg-primary: #f0eee8;
    --bg-tertiary: #e9e6df;
    --bg-hover: #e2dfd7;
    --bg-quinary: #f6f4ee;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --text-quaternary: #c0bab3;
    --border-color: #e3e1db;
    --border-primary: #d5d2cb;
    --border-hover: #cecac4;
    --primary-hover: #7f7a74;
    --primary-active: #726d67;
    --quota-medium-color: #e0aa14;
    --shadow: 0 1px 2px 0 rgb(0 0 0 / 0.08);
    --shadow-lg: 0 10px 18px -3px rgb(0 0 0 / 0.1);
  }
}

* { box-sizing: border-box; margin: 0; padding: 0; }
/* [hidden] must win over any display rule: the panel switches pages with this
   attribute, and a stray display:flex elsewhere would keep a page visible. */
[hidden] { display: none !important; }
html, body { background: var(--bg-secondary); }
body {
  color: var(--text-primary);
  font: 14px/1.55 var(--sans);
  -webkit-font-smoothing: antialiased;
  min-height: 100vh;
}
a { color: var(--primary-active); text-decoration: none; }
code, .mono { font-family: var(--mono); font-size: .93em; }
::selection { background: color-mix(in srgb, var(--primary-color) 30%, transparent); }
:focus-visible { outline: 2px solid var(--primary-color); outline-offset: -2px; border-radius: 4px; }
@media (prefers-reduced-motion: reduce) { * { transition: none !important; animation: none !important; } }

/* ======================= shell ======================= */
/* One surface throughout: header, tabs and page frame all sit on the page
   background, so the panel reads as a single sheet rather than stacked panels. */
.shell { display: block; min-height: 100vh; background: var(--bg-secondary); }

.page-header { padding: 30px 22px 16px; max-width: 1280px; margin: 0 auto; }
.page-header h1 {
  font-size: 20px; font-weight: 650; letter-spacing: -.02em;
  color: var(--text-primary);
}
.page-header .desc {
  color: var(--text-tertiary); font-size: 13px; margin-top: 6px;
  max-width: 62ch; line-height: 1.6;
}

/* ======================= tabs ======================= */
.tabbar {
  position: sticky; top: 0; z-index: 20;
  display: flex; align-items: stretch; gap: 2px;
  padding: 0 22px;
  max-width: 1280px; margin: 0 auto; width: 100%;
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border-color);
  overflow-x: auto; scrollbar-width: none;
}
.tabbar::-webkit-scrollbar { display: none; }
/* The tab: an underline indicator, quiet at rest. Taken from the host's own tab
   component — colour and weight change, plus a 2px bar under the active label. */
.tab {
  position: relative; display: inline-flex; align-items: center; gap: 7px; flex-shrink: 0;
  border: 0; background: none; cursor: pointer;
  padding: 13px 13px 12px; border-radius: 8px 8px 0 0;
  font: 550 13.5px/1 var(--sans); color: var(--text-secondary);
  white-space: nowrap;
  transition: color 200ms ease, background-color 200ms ease;
}
.tab::after {
  content: ''; position: absolute; left: 11px; right: 11px; bottom: -1px;
  height: 2px; border-radius: var(--radius-full); background: transparent;
}
.tab:hover { color: var(--text-primary); background: color-mix(in srgb, var(--bg-tertiary) 55%, transparent); }
.tab.on { color: var(--text-primary); font-weight: 650; }
.tab.on::after { background: var(--text-primary); }

.main { padding: 20px 22px 44px; max-width: 1280px; margin: 0 auto; min-width: 0; }

/* ======================= card ======================= */
/* Matches the host's SectionCard: 14px radius, a very light border, and a mostly
   opaque background so the page tone shows through instead of reading as a pasted
   rectangle. */
.box {
  background: color-mix(in srgb, var(--bg-primary) 88%, transparent);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow);
  margin-bottom: var(--space-md); overflow: hidden;
}
.box > header {
  display: flex; align-items: center; gap: var(--space-sm); flex-wrap: wrap;
  padding: 14px var(--space-lg);
  border-bottom: 1px solid var(--border-color);
}
.box > header h3 {
  font-size: 15px; font-weight: 680; letter-spacing: -.01em;
  display: flex; align-items: baseline; gap: 7px; flex-wrap: wrap;
}
.box > header h3 .hint { font-size: 12px; font-weight: 400; color: var(--text-secondary); }
.box > header .grow { flex: 1; min-width: 0; }
.box > header .note { color: var(--text-secondary); font-size: 12.5px; }
.box .pad { padding: var(--space-lg); }
.box .empty { padding: 28px var(--space-lg); text-align: center; color: var(--text-tertiary); font-size: 13px; }
.box .foot {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 12px var(--space-lg);
  border-top: 1px solid var(--border-color);
  color: var(--text-secondary); font-size: 12.5px;
}
.box .foot .grow { flex: 1; min-width: 0; }
.note { color: var(--text-secondary); font-size: 12px; }
.note.bad { color: var(--error-color); }
.note.ok { color: var(--success-color); }
.grow { flex: 1; min-width: 0; }

.group-head { display: flex; align-items: baseline; gap: 10px; margin: 22px 0 10px; }
.group-head:first-child { margin-top: 0; }
.group-head h2 { font-size: 14px; font-weight: 680; letter-spacing: -.01em; }
.group-head .desc { color: var(--text-secondary); font-size: 12.5px; }

/* ======================= stats ======================= */
/* One card with dividers, not a row of separate cards: this is how the host
   renders a summary strip, and separate cards made it look like four objects of a
   different kind next to the tables below. */
.stats {
  display: grid; grid-template-columns: repeat(auto-fit, minmax(132px, 1fr));
  background: color-mix(in srgb, var(--bg-primary) 88%, transparent);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-card);
  box-shadow: var(--shadow);
  overflow: hidden; margin-bottom: var(--space-md);
}
.stat {
  padding: 16px 18px;
  border-right: 1px solid var(--border-color);
  border-bottom: 1px solid var(--border-color);
}
.stat .v {
  font: 650 24px/1.15 var(--mono); font-variant-numeric: tabular-nums;
  letter-spacing: -.02em; white-space: nowrap;
}
.stat .k { color: var(--text-secondary); font-size: 12px; margin-top: 5px; }
.stat.good .v { color: var(--success-color); }
.stat.warn .v { color: var(--quota-medium-color); }
.stat.bad .v { color: var(--error-color); }

/* ======================= controls ======================= */
button, .btn {
  font: 500 13px/1 var(--sans);
  color: var(--text-primary); background: var(--bg-primary);
  border: 1px solid var(--border-primary); border-radius: var(--radius-md);
  padding: 7px 13px; cursor: pointer; white-space: nowrap;
  transition: background-color 200ms ease, border-color 200ms ease, color 200ms ease;
}
button:hover, .btn:hover { background: var(--bg-hover); border-color: var(--border-hover); }
button:active { transform: translateY(.5px); }
button:disabled { opacity: .55; cursor: not-allowed; }
button.primary, .btn.primary {
  background: var(--primary-color); border-color: var(--primary-color); color: var(--primary-contrast);
}
button.primary:hover { background: var(--primary-hover); border-color: var(--primary-hover); }
button.danger { color: var(--error-color); border-color: color-mix(in srgb, var(--error-color) 40%, transparent); }
button.danger:hover { background: color-mix(in srgb, var(--error-color) 12%, transparent); border-color: var(--error-color); }
button.ghost { background: transparent; border-color: transparent; }
button.ghost:hover { background: var(--bg-tertiary); border-color: transparent; }
button.xs { padding: 5px 10px; font-size: 12.5px; border-radius: 6px; }

/* Adjacent buttons need a gap: inline-block siblings collapse the whitespace
   between them, which made controls look fused. */
header > button + button,
.btn-row > button + button { margin-left: 2px; }

input[type="text"], input[type="password"], input[type="email"],
input[type="number"], input[type="search"], select {
  font: 400 13px/1.4 var(--sans);
  color: var(--text-primary); background: var(--bg-quinary);
  border: 1px solid var(--border-primary); border-radius: var(--radius-md);
  padding: 8px 11px; width: 100%;
  transition: border-color 200ms ease;
}
input:focus, select:focus { border-color: var(--primary-color); outline: none; }
input::placeholder { color: var(--text-quaternary); }

.row { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
.row input[type="password"] { flex: 1 1 260px; min-width: 0; }
.btn-end { display: flex; gap: 8px; flex-shrink: 0; }

/* segmented control */
.seg {
  display: inline-flex; background: var(--bg-tertiary);
  border-radius: var(--radius-full); padding: 3px; gap: 2px; flex-wrap: wrap;
}
.seg button {
  border: 0; background: transparent; border-radius: var(--radius-full);
  padding: 6px 14px; font-size: 13px; color: var(--text-secondary);
}
.seg button:hover { background: transparent; color: var(--text-primary); }
.seg button.on {
  background: var(--bg-primary); color: var(--text-primary);
  font-weight: 650; box-shadow: var(--shadow);
}
.setting-effect {
  color: var(--text-secondary); font-size: 12.5px; margin-top: 12px; line-height: 1.65;
}

/* ======================= table ======================= */
.tbl-wrap {
  overflow-x: auto; -webkit-overflow-scrolling: touch;
  background:
    linear-gradient(to right, var(--bg-primary) 30%, transparent) left / 20px 100% no-repeat,
    linear-gradient(to left, var(--bg-primary) 30%, transparent) right / 20px 100% no-repeat;
  background-attachment: local, local;
}
table { border-collapse: collapse; font-size: 13px; width: 100%; }
th {
  text-align: left; font-weight: 550; font-size: 12px; color: var(--text-secondary);
  padding: 11px 14px; border-bottom: 1px solid var(--border-color); white-space: nowrap;
}
td { padding: 11px 14px; border-bottom: 1px solid var(--border-color); vertical-align: middle; }
tbody tr:last-child td { border-bottom: none; }
tbody tr:hover { background: color-mix(in srgb, var(--bg-tertiary) 70%, transparent); }
th.num, td.num { text-align: right; font-variant-numeric: tabular-nums; white-space: nowrap; }
td.act, th.act { width: 1%; white-space: nowrap; text-align: right; }
td.state, th.state { width: 1%; white-space: nowrap; }

/* Two-line account cell: name on top, detail beneath. Side by side they competed
   for the same width and made the column look ragged. */
.acct-name { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.acct-name strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.pill {
  display: inline-block; padding: 2px 9px; border-radius: var(--radius-full);
  font-size: 11px; font-weight: 600; white-space: nowrap;
}
.pill.on { background: color-mix(in srgb, var(--success-color) 18%, transparent); color: var(--success-color); }
.pill.off { background: color-mix(in srgb, var(--error-color) 16%, transparent); color: var(--error-color); }
.pill.wait { background: color-mix(in srgb, var(--quota-medium-color) 20%, transparent); color: var(--quota-medium-color); }

/* ======================= guide rows ======================= */
/* One card per model. A five-column table could not fit the ratio strings on a
   phone — the last columns were simply cut off — so the parameters are listed
   under the model name instead. */
.guide-row {
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-color);
}
.guide-row:last-child { border-bottom: 0; }
.guide-head { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
.guide-head strong { font-size: 13.5px; }
.guide-meta {
  display: grid;
  grid-template-columns: 58px 1fr;
  gap: 5px 10px;
  margin-top: 9px;
  font-size: 12.5px;
}
.guide-meta dt { color: var(--text-tertiary); }
.guide-meta dd { margin: 0; overflow-wrap: anywhere; }

/* ======================= code block ======================= */
pre.code {
  font-family: var(--mono);
  font-size: 12.5px;
  line-height: 1.6;
  background: var(--bg-tertiary);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  padding: 12px 14px;
  overflow-x: auto;
  white-space: pre;
  color: var(--text-primary);
}
pre.code code, .pad code {
  font-family: var(--mono);
  font-size: .94em;
  background: var(--bg-tertiary);
  border-radius: 4px;
  padding: 1px 5px;
}

/* ======================= trend ======================= */
.trend { display: flex; align-items: flex-end; gap: 3px; height: 120px; padding: 16px var(--space-lg); }
.trend .bar {
  flex: 1; background: var(--primary-color); border-radius: 3px 3px 0 0;
  min-height: 2px; opacity: .75;
}
.trend .bar.bad { background: var(--error-color); }
.trend-legend {
  display: flex; gap: 16px; padding: 0 var(--space-lg) 14px;
  color: var(--text-secondary); font-size: 12px;
}
.trend-legend i { display: inline-block; width: 9px; height: 9px; border-radius: 2px; margin-right: 5px; }
.sw-ok { background: var(--primary-color); }
.sw-bad { background: var(--error-color); }

/* ======================= toast ======================= */
#toasts {
  position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%);
  display: flex; flex-direction: column; gap: 8px; z-index: 50;
  pointer-events: none; max-width: 92vw;
}
.toast {
  background: var(--bg-primary); color: var(--text-primary);
  border: 1px solid var(--border-color); border-radius: var(--radius-md);
  padding: 9px 15px; font-size: 13px; box-shadow: var(--shadow-lg);
  animation: rise 160ms ease;
}
.toast.bad { border-color: var(--error-color); color: var(--error-color); }
.toast.ok { border-color: var(--success-color); color: var(--success-color); }
@keyframes rise { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: none; } }

/* ======================= narrow ======================= */
@media (max-width: 720px) {
  .page-header { padding: 22px 14px 12px; }
  .page-header h1 { font-size: 18px; }
  .tabbar { padding: 0 12px; }
  .tab { padding: 12px 11px 11px; font-size: 13px; }
  .main { padding: 14px 12px 36px; }
  .box > header { padding: 12px 14px; }
  .box .pad, .trend, .trend-legend { padding-left: 14px; padding-right: 14px; }
  .stats { grid-template-columns: repeat(2, 1fr); }
  .stat { padding: 13px 14px; }
  .stat .v { font-size: 20px; }

  /* Touch targets: 44px is the accepted minimum, desktop sizes land near 30px. */
  button, .btn, select,
  input[type=text], input[type=password], input[type=email], input[type=number] { min-height: 40px; }
  button.xs { min-height: 34px; padding: 7px 11px; }
  /* 16px stops iOS from zooming the page when a field is focused. */
  input[type=text], input[type=password], input[type=email], input[type=number] { font-size: 16px; }

  th, td { padding: 10px 8px; }
  table { font-size: 12.5px; }
  /* The account column takes whatever is left after the fixed ones; the four
     fixed cells plus the action button already exceed a 412px viewport if the
     email is allowed to claim its natural width. */
  .acct-name strong { max-width: 108px; }
  td.state, th.state, td.num, th.num { padding-left: 6px; padding-right: 6px; }
  td.act, th.act { padding-left: 4px; padding-right: 8px; }
}
`
