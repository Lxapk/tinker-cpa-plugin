package main

// uiCSS is the panel stylesheet.
//
// Tokens and component shapes follow the reference plugin, which in turn mirrors
// CPA's own management panel: a warm neutral scale where the action colour is a
// quiet grey, green means good and both warning and error are a brick red. The
// panel therefore looks like the surface it lives inside rather than a separate
// app in an iframe.
//
// Matching the host's theme matters more than it sounds: CPA writes
// data-theme="dark" or "white" on its root and removes it for "follow system", so
// the stylesheet keys off the same attributes. Without that, the panel stayed
// light while the host went dark — the most immediately visible defect of a
// hand-rolled stylesheet.
const uiCSS = `
/* ======================= tokens ======================= */
:root,
:root[data-theme="dark"] {
  color-scheme: dark;
  --bg-secondary: #151412;
  --bg-primary: #1d1b18;
  --bg-tertiary: #262320;
  --bg-hover: #2e2a26;

  --text-primary: #f6f4f1;
  --text-secondary: #c9c3bb;
  --text-tertiary: #9c958d;
  --text-quaternary: #6f6962;

  --border-color: #3a3530;
  --border-primary: #4a453f;

  --primary-color: #8b8680;
  --primary-hover: #9a948e;
  --primary-contrast: #ffffff;

  --success-color: #10b981;
  --warning-color: #c65746;
  --error-color: #c65746;

  --shadow: 0 1px 3px 0 rgb(0 0 0 / 0.3);
  --radius-md: 8px;
  --radius-card: 14px;
  --radius-full: 9999px;

  --mono: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  --sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "PingFang SC",
          "Microsoft YaHei", sans-serif;
}

:root[data-theme="white"],
:root[data-theme="light"] {
  color-scheme: light;
  --bg-secondary: #ffffff;
  --bg-primary: #ffffff;
  --bg-tertiary: #f6f6f6;
  --bg-hover: #f0f0f0;

  --text-primary: #2d2a26;
  --text-secondary: #6d6760;
  --text-tertiary: #a29c95;
  --text-quaternary: #c0bab3;

  --border-color: #e5e5e5;
  --border-primary: #d9d9d9;

  --primary-color: #8b8680;
  --primary-hover: #7f7a74;
  --primary-contrast: #ffffff;

  --success-color: #10b981;
  --warning-color: #c65746;
  --error-color: #c65746;

  --shadow: 0 1px 2px 0 rgb(0 0 0 / 0.08);
}

@media (prefers-color-scheme: light) {
  :root:not([data-theme]) {
    color-scheme: light;
    --bg-secondary: #faf9f5;
    --bg-primary: #ffffff;
    --bg-tertiary: #f0eee8;
    --bg-hover: #e9e6df;
    --text-primary: #2d2a26;
    --text-secondary: #6d6760;
    --text-tertiary: #a29c95;
    --text-quaternary: #c0bab3;
    --border-color: #e3e1db;
    --border-primary: #d5d2cb;
    --primary-hover: #7f7a74;
    --shadow: 0 1px 2px 0 rgb(0 0 0 / 0.08);
  }
}

* { box-sizing: border-box; margin: 0; padding: 0; }
[hidden] { display: none !important; }
html, body { background: var(--bg-secondary); }
body {
  color: var(--text-primary);
  font: 14px/1.55 var(--sans);
  -webkit-font-smoothing: antialiased;
  min-height: 100vh;
}
code, .mono { font-family: var(--mono); font-size: .93em; }
:focus-visible { outline: 2px solid var(--primary-color); outline-offset: -2px; border-radius: 4px; }

/* ======================= shell ======================= */
.shell { display: block; min-height: 100vh; background: var(--bg-secondary); }
.page-header { padding: 28px 22px 14px; max-width: 1180px; margin: 0 auto; }
.page-header h1 { font-size: 20px; font-weight: 650; letter-spacing: -.02em; }
.page-header .desc {
  color: var(--text-tertiary); font-size: 13px; margin-top: 6px;
  max-width: 62ch; line-height: 1.6;
}

.tabbar {
  display: flex; gap: 26px; padding: 0 22px;
  max-width: 1180px; margin: 0 auto;
  border-bottom: 1px solid var(--border-color);
  overflow-x: auto; scrollbar-width: none;
}
.tabbar::-webkit-scrollbar { display: none; }
.tab {
  appearance: none; border: 0; background: none; cursor: pointer;
  padding: 11px 0 12px; font: inherit; font-size: 14px;
  color: var(--text-tertiary); white-space: nowrap;
  border-bottom: 2px solid transparent; margin-bottom: -1px;
  transition: color 150ms ease, border-color 150ms ease;
}
.tab:hover { color: var(--text-secondary); }
.tab.on { color: var(--text-primary); font-weight: 600; border-bottom-color: var(--text-primary); }

.main { max-width: 1180px; margin: 0 auto; padding: 20px 22px 56px; }
.view { display: block; }

/* ======================= cards ======================= */
.box {
  background: var(--bg-primary);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-card);
  margin-bottom: 16px;
  overflow: hidden;
}
.box > header {
  display: flex; align-items: center; gap: 10px;
  padding: 14px 16px; border-bottom: 1px solid var(--border-color);
}
.box > header h3 { font-size: 14px; font-weight: 600; }
.box > header .hint { color: var(--text-tertiary); font-size: 12px; font-weight: 400; }
.grow { flex: 1; }
.box .pad { padding: 16px; }
.box .empty { padding: 26px 16px; text-align: center; color: var(--text-tertiary); font-size: 13px; }
.note { color: var(--text-tertiary); font-size: 12px; }
.note.bad { color: var(--error-color); }
.note.ok { color: var(--success-color); }

.group-head { display: flex; align-items: baseline; gap: 10px; margin: 22px 0 10px; }
.group-head:first-child { margin-top: 0; }
.group-head h2 { font-size: 13px; font-weight: 650; letter-spacing: .02em; }
.group-head .desc { color: var(--text-tertiary); font-size: 12px; }

/* ======================= stats ======================= */
.stats {
  display: grid; gap: 10px; margin-bottom: 16px;
  grid-template-columns: repeat(auto-fit, minmax(118px, 1fr));
}
.stat {
  background: var(--bg-primary); border: 1px solid var(--border-color);
  border-radius: var(--radius-card); padding: 14px 15px;
}
.stat .v { font-size: 22px; font-weight: 650; letter-spacing: -.02em; line-height: 1.2; }
.stat .k { color: var(--text-tertiary); font-size: 12px; margin-top: 3px; }
.stat.good .v { color: var(--success-color); }
.stat.warn .v { color: var(--warning-color); }
.stat.bad .v { color: var(--error-color); }

/* ======================= controls ======================= */
button {
  appearance: none; cursor: pointer; font: inherit; font-size: 13px;
  border-radius: var(--radius-md); padding: 7px 13px;
  white-space: nowrap;
  border: 1px solid var(--border-primary);
  background: var(--bg-primary); color: var(--text-primary);
  transition: background 150ms ease, border-color 150ms ease;
}
button:hover { background: var(--bg-hover); }
button:disabled { opacity: .5; cursor: default; }
button.primary {
  background: var(--primary-color); border-color: var(--primary-color);
  color: var(--primary-contrast); font-weight: 600;
}
button.primary:hover { background: var(--primary-hover); border-color: var(--primary-hover); }
button.xs { padding: 5px 10px; font-size: 12px; }
button.danger { color: var(--error-color); }
button.ghost { background: transparent; }

input[type="text"], input[type="password"], input[type="email"], input[type="number"], select {
  font: inherit; font-size: 13px;
  background: var(--bg-tertiary); color: var(--text-primary);
  border: 1px solid var(--border-color); border-radius: var(--radius-md);
  padding: 8px 11px; width: 100%;
}
input::placeholder { color: var(--text-quaternary); }

.row { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
.btn-end { display: flex; gap: 8px; }

/* segmented control */
.seg {
  display: inline-flex; background: var(--bg-tertiary);
  border-radius: var(--radius-full); padding: 3px; gap: 2px;
  flex-wrap: wrap;
}
.seg button {
  border: 0; background: transparent; border-radius: var(--radius-full);
  padding: 6px 14px; font-size: 13px; color: var(--text-secondary);
}
.seg button:hover { background: transparent; color: var(--text-primary); }
.seg button.on {
  background: var(--bg-primary); color: var(--text-primary);
  font-weight: 600; box-shadow: var(--shadow);
}
.setting-effect {
  color: var(--text-tertiary); font-size: 12px; margin-top: 10px; line-height: 1.6;
}

/* ======================= table ======================= */
.table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { text-align: left; padding: 10px 10px; border-bottom: 1px solid var(--border-color); }
/* The action column holds one short button; letting it wrap turned "停用" into a
   vertical stack of two characters. */
td.act, th.act { width: 1%; white-space: nowrap; text-align: right; }
td.state, th.state { width: 1%; white-space: nowrap; }
td.num, th.num { width: 1%; white-space: nowrap; }
th {
  color: var(--text-tertiary); font-weight: 500; font-size: 12px;
  background: var(--bg-tertiary); white-space: nowrap;
}
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover { background: var(--bg-tertiary); }
td.num { font-variant-numeric: tabular-nums; }
.ell { max-width: 210px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* The account cell carries the email; on a phone the ellipsis hid the domain, so
   it wraps onto a second line instead. */
td.who { max-width: 240px; overflow-wrap: anywhere; }

.pill {
  display: inline-block; padding: 2px 9px; border-radius: var(--radius-full);
  font-size: 11px; font-weight: 600;
  /* Without this the badge is squeezed by the table's auto layout and "启用"
     breaks into two stacked characters. */
  white-space: nowrap;
}
.pill.on { background: color-mix(in srgb, var(--success-color) 18%, transparent); color: var(--success-color); }
.pill.off { background: color-mix(in srgb, var(--error-color) 16%, transparent); color: var(--error-color); }
.pill.wait { background: color-mix(in srgb, var(--warning-color) 16%, transparent); color: var(--warning-color); }

/* ======================= trend ======================= */
.trend { display: flex; align-items: flex-end; gap: 3px; height: 120px; padding: 16px; }
.trend .bar { flex: 1; background: var(--primary-color); border-radius: 3px 3px 0 0; min-height: 2px; opacity: .75; }
.trend .bar.bad { background: var(--error-color); }
.trend-legend { display: flex; gap: 16px; padding: 0 16px 14px; color: var(--text-tertiary); font-size: 12px; }
.trend-legend i { display: inline-block; width: 9px; height: 9px; border-radius: 2px; margin-right: 5px; }
.sw-ok { background: var(--primary-color); }
.sw-bad { background: var(--error-color); }

/* ======================= toast ======================= */
#toasts {
  position: fixed; left: 50%; bottom: 24px; transform: translateX(-50%);
  display: flex; flex-direction: column; gap: 8px; z-index: 50;
  pointer-events: none;
}
.toast {
  background: var(--bg-primary); color: var(--text-primary);
  border: 1px solid var(--border-color); border-radius: var(--radius-md);
  padding: 9px 15px; font-size: 13px; box-shadow: var(--shadow);
  animation: rise 160ms ease;
}
.toast.bad { border-color: var(--error-color); color: var(--error-color); }
.toast.ok { border-color: var(--success-color); color: var(--success-color); }
@keyframes rise { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: none; } }

/* ======================= narrow ======================= */
@media (max-width: 560px) {
  .page-header, .main { padding-left: 14px; padding-right: 14px; }
  .tabbar { padding: 0 14px; gap: 18px; }
  /* Two columns: four auto-fit cells produced a 3 + 1 row, which read as a
     mistake rather than a grid. */
  .stats { grid-template-columns: repeat(2, 1fr); }
  .stat .v { font-size: 19px; }
  td.who { max-width: 170px; font-size: 12px; }
  .box > header { flex-wrap: wrap; }
  .box > header .note { order: 3; width: 100%; }
  table { font-size: 12px; }
  th, td { padding: 9px 7px; }
}
`
