package main

// mainPageScript returns the panel's behaviour, wrapper included.
//
// Two ideas keep this small:
//
//  1. Every control declares what it does in data attributes — data-call names
//     the action, data-key / data-arg0 carry its arguments — and one delegated
//     click handler dispatches them. Adding a button therefore means adding an
//     entry to actions{}, not another addEventListener.
//  2. Nothing is fetched until the key exists. The management API rejects
//     anonymous requests, so an eager load would only produce a wall of 401s that
//     read like a plugin fault.
func mainPageScript() string {
	return `<script>
(function () {
  "use strict";

  // CPA stores its own key under "cpa-management-key"; reuse it so the operator
  // never retypes what the panel already knows.
  var PANEL_KEY = "cpa-management-key";
  var OUR_KEY = "tinker.mgmtKey";
  var API = "/v0/management";

  /* ---------- helpers ---------- */

  function key() {
    var names = [PANEL_KEY, OUR_KEY];
    for (var i = 0; i < names.length; i++) {
      try {
        var v = (localStorage.getItem(names[i]) || "").trim();
        if (v) return v;
      } catch (e) {}
    }
    return "";
  }

  function toast(msg, kind) {
    var host = document.getElementById("toasts");
    if (!host) return;
    var el = document.createElement("div");
    el.className = "toast" + (kind ? " " + kind : "");
    el.textContent = msg;
    host.appendChild(el);
    setTimeout(function () { el.remove(); }, 2600);
  }

  function note(id, msg, kind) {
    var el = document.getElementById(id);
    if (!el) return;
    el.textContent = msg || "";
    el.className = "note" + (kind ? " " + kind : "");
  }

  function request(path, options) {
    var k = key();
    if (!k) return Promise.reject(new Error("请先在「设置」里保存管理密钥"));
    var opts = options || {};
    opts.headers = Object.assign({
      "Content-Type": "application/json",
      "Authorization": "Bearer " + k,
      "X-Management-Key": k
    }, opts.headers || {});
    return fetch(API + path, opts).then(function (resp) {
      return resp.text().then(function (body) {
        var data = null;
        try { data = JSON.parse(body); } catch (e) {}
        if (!resp.ok) {
          var msg = (data && (data.message || data.error)) || ("HTTP " + resp.status);
          throw new Error(String(msg));
        }
        return data;
      });
    });
  }

  function reload() { location.reload(); }

  /* ---------- tabs ---------- */

  function showView(name) {
    var views = document.querySelectorAll(".view");
    for (var i = 0; i < views.length; i++) {
      views[i].hidden = views[i].id !== name;
    }
    var tabs = document.querySelectorAll(".tab");
    for (var j = 0; j < tabs.length; j++) {
      tabs[j].classList.toggle("on", tabs[j].dataset.view === name);
    }
    // The two data pages are rendered server-side; switching to them is the
    // moment to refresh, so the numbers are never stale by more than one tab.
    if (name === "view-accounts") refreshAccounts();
    if (name === "view-usage") refreshUsage();
  }

  /* ---------- actions ---------- */

  var actions = {
    refreshAccounts: function () {
      note("accountMsg", "刷新中…");
      request("/tinker/accounts").then(function () {
        note("accountMsg", "已刷新");
        reload();
      }).catch(function (e) { note("accountMsg", e.message, "bad"); });
    },

    toggleAccount: function (btn) {
      var key = btn.dataset.key;
      var disabled = btn.dataset.disabled === "true";
      btn.disabled = true;
      request("/tinker/account/toggle", {
        method: "POST",
        body: JSON.stringify({ key: key, disabled: disabled })
      }).then(function (data) {
        toast((data && data.disabled ? "已停用" : "已启用") + "，正在刷新", "ok");
        setTimeout(reload, 500);
      }).catch(function (e) {
        toast(e.message, "bad");
        btn.disabled = false;
      });
    },

    pickStrategy: function (btn) {
      var value = btn.dataset.arg0;
      var seg = document.getElementById("strategySeg");
      if (seg) {
        var buttons = seg.querySelectorAll("button");
        for (var i = 0; i < buttons.length; i++) {
          buttons[i].classList.toggle("on", buttons[i].dataset.value === value);
        }
      }
      var effect = document.getElementById("strategySegEffect");
      if (effect && effect.dataset[value]) effect.textContent = effect.dataset[value];
      note("strategyMsg", "保存中…");
      request("/tinker/routing/config", {
        method: "POST",
        body: JSON.stringify({ strategy: value })
      }).then(function (data) {
        note("strategyMsg", "已保存", "ok");
        var next = document.getElementById("strategyNext");
        if (next && data && data.hint) next.textContent = data.hint;
        toast("路由策略已更新", "ok");
      }).catch(function (e) {
        note("strategyMsg", e.message, "bad");
        toast(e.message, "bad");
      });
    },

    resetRotation: function () {
      request("/tinker/routing/reset", { method: "POST" }).then(function () {
        toast("轮巡位置已重置", "ok");
      }).catch(function (e) { toast(e.message, "bad"); });
    },

    refreshUsage: function () {
      note("callMsg", "刷新中…");
      request("/tinker/calls").then(function () { reload(); })
        .catch(function (e) { note("callMsg", e.message, "bad"); });
    },

    clearCalls: function () {
      request("/tinker/calls/clear", { method: "POST" }).then(function () {
        toast("调用记录已清空", "ok");
        setTimeout(reload, 400);
      }).catch(function (e) { toast(e.message, "bad"); });
    },

    openLogin: function () {
      request("/tinker/login/new", { method: "POST" }).then(function (data) {
        if (data && data.url) location.href = data.url;
        else toast("未能创建登录会话", "bad");
      }).catch(function (e) { toast(e.message, "bad"); });
    },

    saveKey: function () {
      var input = document.getElementById("mgmtKey");
      var v = ((input && input.value) || "").trim();
      if (!v) { refreshKeyState("请输入密钥", "bad"); return; }
      try { localStorage.setItem(OUR_KEY, v); } catch (e) {
        refreshKeyState("浏览器拒绝保存（可能禁用了 localStorage）", "bad");
        return;
      }
      if (input) input.value = "";
      refreshKeyState();
      toast("密钥已保存到本机浏览器", "ok");
      reload();
    },

    clearKey: function () {
      try { localStorage.removeItem(OUR_KEY); } catch (e) {}
      refreshKeyState();
      toast("已清除本机保存的密钥");
    }
  };

  function refreshKeyState(override, kind) {
    var el = document.getElementById("keyState");
    if (!el) return;
    if (override) {
      el.textContent = override;
      el.className = "note " + (kind || "");
      return;
    }
    var k = key();
    if (k) {
      el.textContent = "已保存密钥（" + k.length + " 字符），操作可直接使用。";
      el.className = "note ok";
    } else {
      el.textContent = "尚未保存密钥：涉及数据的操作会提示缺少管理密钥。";
      el.className = "note";
    }
  }

  /* ---------- wiring ---------- */

  document.addEventListener("click", function (ev) {
    var target = ev.target;
    while (target && target !== document) {
      var name = target.dataset ? target.dataset.call : null;
      if (name && actions[name]) {
        ev.preventDefault();
        actions[name](target);
        return;
      }
      if (target.dataset && target.dataset.view) {
        showView(target.dataset.view);
        return;
      }
      target = target.parentNode;
    }
  });

  refreshKeyState();

  // Load the first page only when a key is present, so a fresh install shows an
  // instruction rather than a burst of failed requests.
  if (key()) {
    refreshAccounts();
    refreshUsage();
  } else {
    note("accountMsg", "请先在「设置」里保存管理密钥", "bad");
  }

  function refreshAccounts() {
    if (!key()) return;
    request("/tinker/accounts").catch(function (e) {
      note("accountMsg", e.message, "bad");
    });
  }

  function refreshUsage() {
    if (!key()) return;
    request("/tinker/calls").catch(function (e) {
      note("callMsg", e.message, "bad");
    });
  }
})();
</script>`
}
