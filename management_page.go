package deepseekcpa

// keysResourcePath serves the browser page for managing DeepSeek credentials.
// The host mounts it at /v0/resource/plugins/deepseek-cpa/keys, which is not
// management-authenticated, so the page itself carries no data: it asks for the
// management key once, stores it in this origin's localStorage, and calls the
// authenticated /v0/management routes from there.
// The page lives at its own path: the host strips the mount prefix before the
// plugin sees it, so sharing "/keys" with the management route would make the
// two indistinguishable.
const keysResourcePath = "/credentials"

const keysPage = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>DeepSeek Keys</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 14px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; margin: 0; padding: 24px; max-width: 780px; }
  h1 { font-size: 18px; margin: 0 0 4px; }
  p.hint { margin: 0 0 20px; opacity: .7; }
  section { border: 1px solid rgba(127,127,127,.35); border-radius: 8px; padding: 16px; margin-bottom: 16px; }
  h2 { font-size: 14px; margin: 0 0 12px; font-weight: 600; }
  label { display: block; font-size: 12px; opacity: .8; margin-bottom: 4px; }
  input { font: inherit; padding: 7px 9px; border-radius: 6px; border: 1px solid rgba(127,127,127,.5); background: transparent; width: 100%; box-sizing: border-box; }
  .row { display: flex; gap: 8px; align-items: flex-end; }
  .row > div { flex: 1; }
  button { font: inherit; padding: 7px 14px; border-radius: 6px; border: 1px solid rgba(127,127,127,.5); background: rgba(127,127,127,.12); cursor: pointer; }
  button:hover:not(:disabled) { background: rgba(127,127,127,.22); }
  button:disabled { opacity: .5; cursor: default; }
  button.danger { border-color: rgba(220,80,80,.6); }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { text-align: left; padding: 7px 8px; border-bottom: 1px solid rgba(127,127,127,.25); }
  th { font-weight: 600; font-size: 12px; opacity: .75; }
  td.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; }
  #status { min-height: 20px; font-size: 13px; }
  #status.error { color: #d9534f; }
  #status.ok { color: #3c9d5d; }
  .empty { opacity: .6; font-size: 13px; }
</style>
</head>
<body>
<h1>DeepSeek Keys</h1>
<p class="hint">密钥由 CLIProxyAPI 托管，保存在宿主的凭证目录里。本页只是调用管理接口的界面，密钥本身不会出现在页面里。</p>

<section id="mgmt-section">
  <h2>管理密钥</h2>
  <p class="hint" id="mgmt-note" style="margin:0 0 10px">CLIProxyAPI 管理密钥。同源面板登录时勾选过「记住密码」的话，这里会自动读取，无需再填。</p>
  <div class="row" id="mgmt-form">
    <div>
      <label for="mgmt">管理密钥</label>
      <input id="mgmt" type="password" autocomplete="off" placeholder="management secret-key">
    </div>
    <button id="save-mgmt">保存</button>
  </div>
  <button id="change-mgmt" style="display:none">更换管理密钥</button>
</section>

<section>
  <h2>现有密钥</h2>
  <div id="keys" class="empty">尚未加载</div>
</section>

<section>
  <h2>添加密钥</h2>
  <div class="row">
    <div>
      <label for="apikey">DeepSeek API key</label>
      <input id="apikey" type="password" autocomplete="off" placeholder="sk-...">
    </div>
    <button id="add">添加</button>
  </div>
</section>

<div id="status"></div>

<script>
(function () {
  var PLUGIN = "deepseek-cpa";
  var STORE = "deepseek-cpa:management-key";
  var BASE = "/v0/management/plugins/" + PLUGIN + "/keys";

  var mgmtInput = document.getElementById("mgmt");
  var apiInput = document.getElementById("apikey");
  var keysBox = document.getElementById("keys");
  var status = document.getElementById("status");

  function setStatus(text, kind) {
    status.textContent = text;
    status.className = kind || "";
  }

  // The management panel persists its auth store under localStorage["cli-proxy-auth"]
  // as a zustand record {state, version}, obfuscated as "enc::v1::" +
  // base64(XOR(utf8(JSON), key)) where key is derived from the page host, the
  // user agent and a fixed salt. Reading it here means a user who ticked
  // "remember password" in the panel never retypes it.
  //
  // The standalone "managementKey" entry is the older panel layout, kept only as
  // a migration fallback, and is read only when the current record is absent. An
  // empty, corrupt, logged-out or foreign-server record must not resurrect a
  // stale password.
  var PANEL_AUTH_KEY = "cli-proxy-auth";
  var LEGACY_PANEL_KEY = "managementKey";
  var OBFUSCATION_PREFIX = "enc::v1::";
  var OBFUSCATION_SALT = "cli-proxy-api-webui::secure-storage";

  function decodePanelRecord(raw) {
    if (!raw || raw.indexOf(OBFUSCATION_PREFIX) !== 0) {
      return undefined;
    }
    try {
      var key = new TextEncoder().encode(
        OBFUSCATION_SALT + "|" + window.location.host + "|" + navigator.userAgent
      );
      var binary = atob(raw.slice(OBFUSCATION_PREFIX.length));
      var bytes = new Uint8Array(binary.length);
      for (var i = 0; i < binary.length; i++) {
        bytes[i] = binary.charCodeAt(i) ^ key[i % key.length];
      }
      return JSON.parse(new TextDecoder().decode(bytes));
    } catch (e) {
      // A different panel version, a changed user agent, or a rotated record: let
      // the caller ask instead of guessing.
      return undefined;
    }
  }

  function normalizeManagementKey(value) {
    if (typeof value !== "string") {
      return "";
    }
    var trimmed = value.trim();
    // A mismatched host or user agent turns the tail into garbage that can still
    // parse; only accept something shaped like a management key, because a wrong
    // guess costs one of the host's five failed attempts.
    return /^[\x21-\x7e]{4,256}$/.test(trimmed) ? trimmed : "";
  }

  // The page calls /v0/management on its own origin at the root of the host, so
  // only a panel apiBase naming that same root may supply the key. A base with a
  // path prefix, credentials, query or fragment names a different destination
  // and would send this page's requests somewhere the panel never logged into.
  function isSamePanelBase(apiBase) {
    if (typeof apiBase !== "string" || !apiBase.trim()) {
      return false;
    }
    try {
      var base = new URL(apiBase.trim());
      if (base.origin !== window.location.origin) {
        return false;
      }
      if (base.username || base.password || base.search || base.hash) {
        return false;
      }
      return base.pathname.replace(/\/+$/, "") === "";
    } catch (e) {
      return false;
    }
  }

  function panelManagementKey() {
    var raw;
    try {
      raw = localStorage.getItem(PANEL_AUTH_KEY);
    } catch (e) {
      return "";
    }
    if (raw != null) {
      var auth = decodePanelRecord(raw);
      if (!auth || typeof auth !== "object" || Array.isArray(auth)) {
        return "";
      }
      var state = auth.state;
      if (!state || typeof state !== "object" || Array.isArray(state)) {
        return "";
      }
      if (state.rememberPassword !== true || !isSamePanelBase(state.apiBase)) {
        return "";
      }
      return normalizeManagementKey(state.managementKey);
    }

    var legacy;
    try {
      legacy = localStorage.getItem(LEGACY_PANEL_KEY);
    } catch (e) {
      return "";
    }
    return normalizeManagementKey(decodePanelRecord(legacy));
  }

  function managementKey() {
    return mgmtInput.value || panelManagementKey() || localStorage.getItem(STORE) || "";
  }

  function call(method, path, body) {
    var key = managementKey();
    if (!key) {
      return Promise.reject(new Error("请先填写管理密钥"));
    }
    var options = { method: method, headers: { "X-Management-Key": key } };
    if (body !== undefined) {
      options.headers["Content-Type"] = "application/json";
      options.body = JSON.stringify(body);
    }
    return fetch(path, options).then(function (response) {
      return response.text().then(function (text) {
        var payload = null;
        try { payload = text ? JSON.parse(text) : null; } catch (e) { payload = null; }
        if (!response.ok) {
          var message = (payload && (payload.error || payload.message)) || text || ("HTTP " + response.status);
          if (typeof message !== "string") { message = JSON.stringify(message); }
          throw new Error(message);
        }
        return payload;
      });
    });
  }

  function renderKeys(keys) {
    keysBox.textContent = "";
    keysBox.className = "";
    if (!keys || !keys.length) {
      keysBox.className = "empty";
      keysBox.textContent = "还没有添加任何密钥。";
      return;
    }
    var table = document.createElement("table");
    var head = document.createElement("tr");
    ["名称", "状态", "可用", ""].forEach(function (title) {
      var th = document.createElement("th");
      th.textContent = title;
      head.appendChild(th);
    });
    table.appendChild(head);
    keys.forEach(function (item) {
      var row = document.createElement("tr");
      var name = document.createElement("td");
      name.className = "mono";
      name.textContent = item.name || "";
      row.appendChild(name);
      var st = document.createElement("td");
      st.textContent = item.status || (item.disabled ? "disabled" : "active");
      row.appendChild(st);
      var avail = document.createElement("td");
      avail.textContent = item.available ? "是" : "否";
      row.appendChild(avail);
      var action = document.createElement("td");
      var remove = document.createElement("button");
      remove.className = "danger";
      remove.textContent = "删除";
      remove.addEventListener("click", function () {
        if (!confirm("删除 " + (item.name || "该密钥") + "？")) { return; }
        remove.disabled = true;
        call("DELETE", BASE + "?name=" + encodeURIComponent(item.name))
          .then(function () { setStatus("已删除 " + item.name, "ok"); return load(); })
          .catch(function (error) { remove.disabled = false; setStatus(error.message, "error"); });
      });
      action.appendChild(remove);
      row.appendChild(action);
      table.appendChild(row);
    });
    keysBox.appendChild(table);
  }

  function load() {
    if (!managementKey()) {
      keysBox.className = "empty";
      keysBox.textContent = "填写管理密钥后加载。";
      showKeyForm();
      return Promise.resolve();
    }
    return call("GET", BASE)
      .then(function (payload) {
        renderKeys(payload && payload.keys);
        hideKeyForm();
      })
      .catch(function (error) {
        keysBox.className = "empty";
        keysBox.textContent = "加载失败。";
        setStatus(error.message, "error");
        showKeyForm("管理密钥无效或已变更，请重新输入。");
      });
  }

  var mgmtForm = document.getElementById("mgmt-form");
  var mgmtNote = document.getElementById("mgmt-note");
  var changeButton = document.getElementById("change-mgmt");

  function showKeyForm(note) {
    mgmtForm.style.display = "";
    changeButton.style.display = "none";
    mgmtNote.textContent = note || "CLIProxyAPI 管理密钥。本插件只在本机浏览器保存一次，之后不再询问。";
  }

  function hideKeyForm() {
    mgmtForm.style.display = "none";
    changeButton.style.display = "";
    mgmtNote.textContent = panelManagementKey()
      ? "已使用面板记住的管理密钥（勾选了「记住密码」）。"
      : "已使用本机保存的管理密钥。";
  }

  document.getElementById("save-mgmt").addEventListener("click", function () {
    var value = mgmtInput.value.trim();
    if (!value) { setStatus("管理密钥不能为空", "error"); return; }
    localStorage.setItem(STORE, value);
    setStatus("管理密钥已保存在本机浏览器", "ok");
    load().then(function () {
      // Only hide the form once the stored key actually worked: a wrong key
      // counts toward the host's failed-attempt ban, so it must stay visible.
      if (status.className === "ok") { hideKeyForm(); } else { showKeyForm("密钥无效，请重新输入。"); }
    });
  });

  changeButton.addEventListener("click", function () {
    mgmtInput.value = "";
    localStorage.removeItem(STORE);
    showKeyForm("输入新的管理密钥。面板里勾选过「记住密码」时，下次仍会优先读面板的值。");
    mgmtInput.focus();
  });

  document.getElementById("add").addEventListener("click", function () {
    var apiKey = apiInput.value.trim();
    if (!apiKey) { setStatus("请填写 DeepSeek API key", "error"); return; }
    var button = document.getElementById("add");
    button.disabled = true;
    call("POST", BASE, { api_key: apiKey })
      .then(function (payload) {
        apiInput.value = "";
        setStatus("已添加 " + (payload && payload.name ? payload.name : "密钥"), "ok");
        return load();
      })
      .catch(function (error) { setStatus(error.message, "error"); })
      .then(function () { button.disabled = false; });
  });

  var fromPanel = panelManagementKey();
  var saved = localStorage.getItem(STORE);
  if (fromPanel) {
    mgmtForm.style.display = "none";
    mgmtNote.textContent = "已使用面板记住的管理密钥（勾选了「记住密码」）。";
    load();
  } else if (saved) {
    mgmtInput.value = saved;
    mgmtForm.style.display = "none";
    mgmtNote.textContent = "正在使用本机保存的管理密钥…";
    load();
  } else {
    showKeyForm();
    load();
  }
})();
</script>
</body>
</html>
`
