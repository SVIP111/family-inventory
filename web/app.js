/* 家庭物资管理前端逻辑（原生 JS，无构建依赖） */

const state = {
  locations: [],
  byId: {},
  children: {},
  tags: [],
  items: [],
  filter: { location: "", tag: "", q: "" },
  view: "grid", // grid | list
  page: "gallery", // gallery | loc
  editingId: null,
  expanded: new Set(),
  pendingParentId: "",
  pendingLocImage: "",
  selectMode: false,
  selected: new Set(),
};

const $ = (id) => document.getElementById(id);

// ---------- 登录态 ----------

const TOKEN_KEY = "fi_token";
const USER_KEY = "fi_user";
let authToken = localStorage.getItem(TOKEN_KEY) || "";
let authUser = localStorage.getItem(USER_KEY) || "";
// 当前鉴权模式：login | register
let authMode = "login";

function clearAuth() {
  authToken = "";
  authUser = "";
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(USER_KEY);
}

function saveAuth(token, username) {
  authToken = token;
  authUser = username;
  localStorage.setItem(TOKEN_KEY, token);
  localStorage.setItem(USER_KEY, username);
}

// ---------- 通用请求 ----------

async function api(method, url, body, isForm) {
  const opts = { method };
  const headers = {};
  if (authToken) headers["Authorization"] = "Bearer " + authToken;
  if (body) {
    if (isForm) {
      opts.body = body;
    } else {
      headers["Content-Type"] = "application/json";
      opts.body = JSON.stringify(body);
    }
  }
  opts.headers = headers;
  const res = await fetch(url, opts);
  const text = await res.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch (_) { data = null; }
  if (res.status === 401) {
    clearAuth();
    showAuthScreen(true);
    throw new Error("登录已过期，请重新登录");
  }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

let toastTimer = null;
function toast(msg, isError) {
  const el = $("toast");
  el.textContent = msg;
  el.className = "toast" + (isError ? " error" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add("hidden"), 2600);
}

// ---------- 登录 / 注册 ----------

// registered=false 时进入注册模式（首次使用），否则为登录模式。
function showAuthScreen(registered) {
  document.body.classList.remove("authed");
  $("auth-screen").classList.remove("hidden");
  authMode = registered ? "login" : "register";
  $("auth-sub").textContent = registered ? "请登录后使用" : "首次使用，请注册管理员账号";
  $("auth-submit").textContent = registered ? "登录" : "注册并进入";
  $("auth-confirm-field").hidden = registered;
  $("auth-confirm").required = !registered;
  $("auth-password").autocomplete = registered ? "current-password" : "new-password";
  $("auth-error").hidden = true;
  $("auth-username").focus();
}

function showApp() {
  document.body.classList.add("authed");
  $("auth-screen").classList.add("hidden");
  const chip = $("user-chip");
  if (authUser) {
    chip.textContent = "👤 " + authUser;
    chip.classList.remove("hidden");
  }
}

function authError(msg) {
  const el = $("auth-error");
  el.textContent = msg;
  el.hidden = false;
}

// 启动时校验登录态；已登录返回 true，否则展示登录/注册页。
async function bootAuth() {
  let status;
  try {
    status = await api("GET", "/api/auth/status");
  } catch (_) {
    status = { registered: true, authenticated: false };
  }
  if (status.authenticated && authToken) {
    showApp();
    return true;
  }
  clearAuth();
  showAuthScreen(status.registered);
  return false;
}

async function logout() {
  try { await api("POST", "/api/auth/logout"); } catch (_) {}
  clearAuth();
  showAuthScreen(true);
}

// ---------- 数据加载 ----------

async function loadLocations() {
  state.locations = await api("GET", "/api/locations");
  rebuildLocationIndex();
  populateLocationOptions();
  renderLocationList();
}

async function loadTags() {
  state.tags = await api("GET", "/api/tags");
  populateTagOptions();
}

async function loadItems() {
  const params = new URLSearchParams();
  if (state.filter.location) params.set("location", state.filter.location);
  if (state.filter.tag) params.set("tag", state.filter.tag);
  if (state.filter.q) params.set("q", state.filter.q);
  const qs = params.toString();
  state.items = await api("GET", "/api/items" + (qs ? "?" + qs : ""));
  renderGallery();
  syncLocImageButton();
}

async function loadStats() {
  const stats = await api("GET", "/api/stats");
  $("stats-line").textContent =
    `共 ${stats.items} 件物品 · ${stats.locations} 个位置 · ${stats.withImage} 件已配图`;
}

async function refreshAll() {
  await Promise.all([loadLocations(), loadTags(), loadItems(), loadStats()]);
}

// ---------- 位置树索引 ----------

function rebuildLocationIndex() {
  state.byId = {};
  state.children = {};
  state.locations.forEach((l) => {
    state.byId[l.id] = l;
    const p = l.parentId || "";
    (state.children[p] ||= []).push(l);
  });
  Object.values(state.children).forEach((arr) =>
    arr.sort((a, b) => a.name.localeCompare(b.name, "zh"))
  );
}

// 向上拼接完整路径，如：主卧 / 主卧衣柜 / 第一层
function pathOf(id) {
  const parts = [];
  let cur = state.byId[id];
  while (cur) {
    parts.unshift(cur.name);
    cur = cur.parentId ? state.byId[cur.parentId] : null;
  }
  return parts.join(" / ");
}

// 按完整路径反查位置 ID（忽略首尾空格）。
function findByPath(text) {
  const t = String(text || "").trim();
  if (!t) return null;
  for (const l of state.locations) {
    if (pathOf(l.id) === t) return l;
  }
  return null;
}

// 收集某节点及其全部后代 ID。
function subtreeIDs(rootId) {
  const out = new Set([rootId]);
  const walk = (id) => {
    (state.children[id] || []).forEach((c) => {
      if (!out.has(c.id)) {
        out.add(c.id);
        walk(c.id);
      }
    });
  };
  walk(rootId);
  return out;
}

function subtreeItemCount(id) {
  const ids = subtreeIDs(id);
  return state.items.filter((x) => ids.has(x.locationId)).length;
}

// 当前位置筛选命中的位置（按 ID 或完整路径反查）。
function currentFilterLocation() {
  const v = state.filter.location;
  if (!v) return null;
  if (state.byId[v]) return state.byId[v];
  return findByPath(v);
}

// 仅当筛选位置已上传布局图时，显示「查看位置图」按钮。
function syncLocImageButton() {
  const btn = $("btn-view-loc-image");
  const loc = currentFilterLocation();
  btn.hidden = !(loc && loc.imageFile);
}

// ---------- 补全选项 ----------

function populateLocationOptions() {
  const list = $("location-options");
  list.innerHTML = state.locations
    .map((l) => `<option value="${esc(pathOf(l.id))}">`)
    .join("");
}

function populateTagOptions() {
  const list = $("tag-options");
  list.innerHTML = state.tags
    .map((t) => `<option value="${esc(t)}">`)
    .join("");
}

// ---------- 位置管理树渲染 ----------

function renderLocationList() {
  const ul = $("location-list");
  const roots = state.children[""] || [];
  if (!state.locations.length) {
    ul.innerHTML = `<li><span class="loc-name">暂无位置，可在上方添加根位置，或在某节点下添加子位置。</span></li>`;
    return;
  }
  ul.innerHTML = roots.map((l) => renderNode(l, 0)).join("");
}

function renderNode(loc, depth) {
  const kids = state.children[loc.id] || [];
  const expanded = state.expanded.has(loc.id);
  const count = subtreeItemCount(loc.id);
  const blocked = count > 0 || kids.length > 0;
  const toggle = kids.length
    ? `<button type="button" class="tree-toggle" data-toggle="${esc(loc.id)}">${expanded ? "▾" : "▸"}</button>`
    : `<span class="tree-toggle tree-toggle-empty"></span>`;
  const thumb = loc.imageFile
    ? `<img class="loc-thumb" src="/uploads/${esc(loc.imageFile)}" alt="布局图" data-preview-loc="${esc(loc.id)}" />`
    : "";
  const kidsHtml = expanded ? kids.map((k) => renderNode(k, depth + 1)).join("") : "";
  return `
    <li class="tree-node">
      <div class="tree-row" style="padding-left:${depth * 18}px">
        ${toggle}
        ${thumb}
        <span class="loc-name">${esc(loc.name)}</span>
        <span class="loc-count">${count} 件</span>
        <span class="tree-actions">
          <button type="button" class="btn ghost small" data-loc-image="${esc(loc.id)}" title="上传 / 更换布局图">🖼️ 布局图</button>
          <button type="button" class="btn ghost small" data-qr-loc="${esc(loc.id)}" title="查看位置二维码">▦ 二维码</button>
          <button type="button" class="btn ghost small" data-add-child="${esc(loc.id)}">＋子位置</button>
          <button type="button" class="btn danger-ghost small" data-del-loc="${esc(loc.id)}"
            ${blocked ? "disabled" : ""} title="${blocked ? "存在物品或子位置，无法删除" : "删除该位置"}">删除</button>
        </span>
      </div>
      ${kidsHtml}
    </li>`;
}

// ---------- 物品卡片渲染 ----------

function cardImage(item) {
  if (item.imageFile) {
    return `<img class="card-img" src="/uploads/${esc(item.imageFile)}" alt="${esc(item.name)}" loading="lazy" />`;
  }
  return `<div class="card-img placeholder">📦</div>`;
}

function tagBadges(tags) {
  if (!tags || !tags.length) return "";
  return `<div class="tags">` +
    tags.map((t) => `<span class="tag">${esc(t)}</span>`).join("") +
    `</div>`;
}

function selectCheck(id) {
  return `<label class="select-check" data-select="${esc(id)}"><input type="checkbox" ${state.selected.has(id) ? "checked" : ""} aria-label="选择物品" /></label>`;
}

function gridCard(it) {
  return `
    <article class="card" data-id="${esc(it.id)}">
      ${selectCheck(it.id)}
      ${cardImage(it)}
      <div class="card-body">
        <div class="card-title">${esc(it.name)}</div>
        ${it.description ? `<div class="card-desc">${esc(it.description)}</div>` : ""}
        <div class="card-meta">
          <span class="badge">${esc(pathOf(it.locationId) || it.locationName || "未分配位置")}</span>
          <span class="qty">数量 ×${it.quantity}</span>
        </div>
        ${it.barcode ? `<div class="card-barcode">🔖 ${esc(it.barcode)}</div>` : ""}
        ${tagBadges(it.tags)}
      </div>
      <div class="card-actions">
        <button type="button" class="btn ghost small" data-qr-item="${esc(it.id)}" title="查看物品二维码">▦ 二维码</button>
        <button type="button" class="btn ghost small" data-edit="${esc(it.id)}">编辑</button>
        <button type="button" class="btn danger-ghost small" data-del="${esc(it.id)}">删除</button>
      </div>
    </article>`;
}

function listRow(it) {
  return `
    <article class="row" data-id="${esc(it.id)}">
      ${selectCheck(it.id)}
      <div class="row-img">${cardImage(it)}</div>
      <div class="row-main">
        <div class="row-title">${esc(it.name)}<span class="qty">×${it.quantity}</span></div>
        <div class="card-meta">
          <span class="badge">${esc(pathOf(it.locationId) || it.locationName || "未分配位置")}</span>
          ${it.barcode ? `<span class="card-barcode inline">🔖 ${esc(it.barcode)}</span>` : ""}
          ${tagBadges(it.tags)}
        </div>
        ${it.description ? `<div class="card-desc">${esc(it.description)}</div>` : ""}
      </div>
      <div class="row-actions">
        <button type="button" class="btn ghost small" data-qr-item="${esc(it.id)}" title="查看物品二维码">▦ 二维码</button>
        <button type="button" class="btn ghost small" data-edit="${esc(it.id)}">编辑</button>
        <button type="button" class="btn danger-ghost small" data-del="${esc(it.id)}">删除</button>
      </div>
    </article>`;
}

function renderGallery() {
  const gallery = $("gallery");
  $("result-count").textContent = `显示 ${state.items.length} 件`;

  $("view-grid").classList.toggle("active", state.view === "grid");
  $("view-list").classList.toggle("active", state.view === "list");
  gallery.classList.toggle("grid-view", state.view === "grid");
  gallery.classList.toggle("list-view", state.view === "list");

  if (!state.items.length) {
    gallery.innerHTML = `<div class="empty"><div class="big">🗂️</div>暂无物品，点击右上角「新增物品」开始登记。</div>`;
    return;
  }

  gallery.innerHTML = state.items
    .map((it) => (state.view === "list" ? listRow(it) : gridCard(it)))
    .join("");
}

// ---------- 批量选择 / 删除 ----------

function syncBulkBar() {
  $("bulk-count").textContent = `已选 ${state.selected.size} 件`;
  $("bulk-select-all").checked = state.items.length > 0 &&
    state.items.every((x) => state.selected.has(x.id));
}

function setSelectMode(on) {
  state.selectMode = on;
  if (!on) state.selected.clear();
  document.body.classList.toggle("select-mode", on);
  $("bulk-bar").classList.toggle("hidden", !on);
  renderGallery();
  syncBulkBar();
}

function toggleSelect(id) {
  if (state.selected.has(id)) state.selected.delete(id);
  else state.selected.add(id);
  const cb = document.querySelector(`[data-select="${CSS.escape(id)}"] input`);
  if (cb) cb.checked = state.selected.has(id);
  syncBulkBar();
}

// ---------- 弹窗控制 ----------

function openModal(id) { $(id).classList.remove("hidden"); }
function closeModal(id) { $(id).classList.add("hidden"); }

function resetItemForm() {
  $("item-form").reset();
  $("item-form").quantity.value = "1";
  $("item-location-id").value = "";
  const preview = $("image-preview");
  preview.classList.add("hidden");
  preview.removeAttribute("src");
  state.editingId = null;
  $("item-modal-title").textContent = "新增物品";
}

function openAddItem() {
  resetItemForm();
  openModal("item-modal");
}

function openEditItem(id) {
  const it = state.items.find((x) => x.id === id);
  if (!it) return;
  state.editingId = id;
  $("item-modal-title").textContent = "编辑物品";
  $("item-form").name.value = it.name;
  $("item-form").locationName.value = pathOf(it.locationId) || it.locationName || "";
  $("item-location-id").value = it.locationId || "";
  $("item-form").quantity.value = it.quantity;
  $("item-form").description.value = it.description || "";
  $("item-form").tags.value = (it.tags || []).join(", ");
  $("item-form").barcode.value = it.barcode || "";
  const preview = $("image-preview");
  if (it.imageFile) {
    preview.src = "/uploads/" + it.imageFile;
    preview.classList.remove("hidden");
  } else {
    preview.classList.add("hidden");
  }
  openModal("item-modal");
}

// 位置输入框：选中已有路径则回填节点 ID，否则清空（视为新根位置）。
function bindLocationInput(textInputId, hiddenId) {
  const input = $(textInputId);
  input.addEventListener("input", () => {
    const loc = findByPath(input.value);
    $(hiddenId).value = loc ? loc.id : "";
  });
}

// 筛选/搜索框一键清除：有内容时显示 ×，点击清空并回调重置状态。
function bindClear(inputId, onClear) {
  const input = $(inputId);
  const btn = document.querySelector(`[data-clear="${inputId}"]`);
  if (!input || !btn) return;
  const sync = () => { btn.hidden = !input.value; };
  input.addEventListener("input", sync);
  btn.addEventListener("click", () => {
    input.value = "";
    sync();
    onClear();
  });
  sync();
}

// ---------- 扫码录入 / 扫码搜索 ----------

let scanStream = null;
let scanTimer = null;

function stopScan() {
  if (scanTimer) { clearTimeout(scanTimer); scanTimer = null; }
  if (scanStream) { scanStream.getTracks().forEach((t) => t.stop()); scanStream = null; }
  const video = $("scan-video");
  if (video) video.srcObject = null;
}

function closeScan() {
  stopScan();
  closeModal("scan-modal");
}

// onDetect(识别结果)：识别成功并关闭弹窗后回调。
async function startScan(onDetect) {
  if (!("BarcodeDetector" in window)) {
    toast("当前浏览器不支持扫码，请手动输入条码", true);
    return;
  }
  openModal("scan-modal");
  const video = $("scan-video");
  const status = $("scan-status");
  status.textContent = "";
  try {
    scanStream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" }, audio: false });
    video.srcObject = scanStream;
    await video.play();
  } catch (err) {
    status.textContent = "无法打开摄像头：" + (err && err.message ? err.message : "权限被拒绝");
    stopScan();
    return;
  }
  let detector;
  try {
    detector = new BarcodeDetector({
      formats: ["qr_code", "ean_13", "ean_8", "code_128", "code_39", "upc_a", "upc_e", "itf", "codabar", "data_matrix"],
    });
  } catch (_) {
    detector = new BarcodeDetector();
  }
  const tick = async () => {
    if (!scanStream) return;
    if (video.readyState < 2) { scanTimer = setTimeout(tick, 200); return; }
    try {
      const codes = await detector.detect(video);
      if (codes && codes.length && codes[0].rawValue) {
        const value = codes[0].rawValue;
        closeScan();
        if (typeof onDetect === "function") onDetect(value);
        return;
      }
    } catch (_) {}
    scanTimer = setTimeout(tick, 250);
  };
  tick();
}

// 扫码结果路由：本应用链接 → 打开物品 / 位置；其余 → 按条码关键字搜索。
function handleScanValue(value) {
  value = String(value || "").trim();
  if (!value) return;
  let parsed = null;
  try { parsed = new URL(value); } catch (_) { parsed = null; }
  if (parsed && parsed.origin === location.origin) {
    const itemId = parsed.searchParams.get("item");
    const locId = parsed.searchParams.get("loc");
    if (itemId) { openItemQR(itemId); return; }
    if (locId) { applyLocationFilter(locId); return; }
  }
  $("search").value = value;
  state.filter.q = value;
  syncClearBtn("search");
  loadItems();
  toast("已扫码搜索：" + value);
}

function syncClearBtn(inputId) {
  const btn = document.querySelector(`[data-clear="${inputId}"]`);
  if (btn) btn.hidden = !$(inputId).value;
}

function applyLocationFilter(locId) {
  const loc = state.byId[locId];
  state.filter.location = locId;
  $("location-filter").value = loc ? pathOf(locId) : locId;
  syncClearBtn("location-filter");
  loadItems();
}

// ---------- 物品详情弹窗 ----------

function formatTime(ts) {
  if (!ts) return "—";
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const pad = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

let detailId = null;

async function openDetail(id) {
  let it = state.items.find((x) => x.id === id);
  if (!it) {
    try { it = await api("GET", "/api/items/" + id); }
    catch (err) { toast(err.message, true); return; }
  }
  detailId = id;
  $("detail-title").textContent = it.name;
  const img = $("detail-img");
  if (it.imageFile) {
    img.src = "/uploads/" + it.imageFile;
    img.classList.remove("hidden");
  } else {
    img.classList.add("hidden");
    img.removeAttribute("src");
  }
  $("detail-location").textContent = pathOf(it.locationId) || it.locationName || "未分配位置";
  $("detail-quantity").textContent = "×" + it.quantity;
  $("detail-barcode").textContent = it.barcode || "—";
  $("detail-tags").textContent = (it.tags && it.tags.length) ? it.tags.join(", ") : "—";
  $("detail-description").textContent = it.description || "（无备注）";
  $("detail-created").textContent = formatTime(it.createdAt);
  $("detail-updated").textContent = formatTime(it.updatedAt);
  openModal("detail-modal");
}

// ---------- 二维码弹窗 ----------

function qrImageURL(text) {
  return "/api/qr?text=" + encodeURIComponent(text);
}

async function openItemQR(id) {
  let it = state.items.find((x) => x.id === id);
  if (!it) {
    try { it = await api("GET", "/api/items/" + id); }
    catch (err) { toast(err.message, true); return; }
  }
  const url = location.origin + "/?item=" + encodeURIComponent(it.id);
  $("qr-title").textContent = "物品二维码";
  $("qr-meta").textContent = `${it.name} · ${pathOf(it.locationId) || it.locationName || "未分配位置"} · 数量 ×${it.quantity}`;
  $("qr-url").textContent = url;
  $("qr-img").src = qrImageURL(url);
  $("qr-download").href = qrImageURL(url);
  $("qr-download").download = "物品-" + it.name + ".png";
  openModal("qr-modal");
}

function openLocQR(id) {
  const loc = state.byId[id];
  if (!loc) return;
  const url = location.origin + "/?loc=" + encodeURIComponent(id);
  $("qr-title").textContent = "位置二维码";
  $("qr-meta").textContent = pathOf(id);
  $("qr-url").textContent = url;
  $("qr-img").src = qrImageURL(url);
  $("qr-download").href = qrImageURL(url);
  $("qr-download").download = "位置-" + loc.name + ".png";
  openModal("qr-modal");
}

// ---------- 二维码标签批量打印 ----------

function qrLabelHTML(target, name, sub) {
  return `<div class="qr-label">
    <img src="${qrImageURL(target)}" alt="二维码" />
    <div class="qr-label-name">${esc(name)}</div>
    ${sub ? `<div class="qr-label-sub">${esc(sub)}</div>` : ""}
  </div>`;
}

// kind: item = 勾选物品；loc = 全部位置
function openPrintQR(kind) {
  const labels = [];
  if (kind === "item") {
    const items = state.items.filter((x) => state.selected.has(x.id));
    if (!items.length) { toast("请先在批量管理中勾选要导出的物品", true); return; }
    items.forEach((it) => {
      const url = location.origin + "/?item=" + encodeURIComponent(it.id);
      labels.push(qrLabelHTML(url, it.name, pathOf(it.locationId) || it.locationName || ""));
    });
  } else {
    if (!state.locations.length) { toast("还没有位置", true); return; }
    state.locations.forEach((l) => {
      const url = location.origin + "/?loc=" + encodeURIComponent(l.id);
      labels.push(qrLabelHTML(url, l.name, pathOf(l.id)));
    });
  }
  $("print-sheet").innerHTML = labels.join("");
  openModal("print-modal");
}

$("btn-print-qr").addEventListener("click", () => openPrintQR("item"));
$("btn-print-loc-qr").addEventListener("click", () => openPrintQR("loc"));
$("btn-print").addEventListener("click", () => window.print());

// ---------- 位置布局图 ----------

function openLocImage(id) {
  const loc = state.byId[id];
  if (!loc || !loc.imageFile) return;
  $("image-modal-img").src = "/uploads/" + loc.imageFile;
  openModal("image-modal");
}

// ---------- 事件绑定 ----------

let searchTimer = null;
$("search").addEventListener("input", (e) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    state.filter.q = e.target.value.trim();
    loadItems();
  }, 250);
});

let locTimer = null;
$("location-filter").addEventListener("input", (e) => {
  clearTimeout(locTimer);
  locTimer = setTimeout(() => {
    const loc = findByPath(e.target.value);
    state.filter.location = loc ? loc.id : e.target.value.trim();
    loadItems();
  }, 250);
});

let tagTimer = null;
$("tag-filter").addEventListener("input", (e) => {
  clearTimeout(tagTimer);
  tagTimer = setTimeout(() => {
    state.filter.tag = e.target.value.trim();
    loadItems();
  }, 250);
});

bindClear("search", () => { state.filter.q = ""; loadItems(); });
bindClear("location-filter", () => { state.filter.location = ""; loadItems(); });
bindClear("tag-filter", () => { state.filter.tag = ""; loadItems(); });

$("view-grid").addEventListener("click", () => { state.view = "grid"; renderGallery(); });
$("view-list").addEventListener("click", () => { state.view = "list"; renderGallery(); });

$("btn-add").addEventListener("click", openAddItem);
$("btn-logout").addEventListener("click", logout);

// 批量管理模式
$("btn-select-mode").addEventListener("click", () => setSelectMode(true));
$("btn-exit-select").addEventListener("click", () => setSelectMode(false));
$("bulk-select-all").addEventListener("change", (e) => {
  if (e.target.checked) state.items.forEach((x) => state.selected.add(x.id));
  else state.selected.clear();
  renderGallery();
  syncBulkBar();
});
$("btn-bulk-delete").addEventListener("click", async () => {
  if (!state.selected.size) { toast("请先勾选要删除的物品", true); return; }
  if (!confirm(`确定删除选中的 ${state.selected.size} 件物品吗？图片将一并删除。`)) return;
  let ok = 0;
  for (const id of [...state.selected]) {
    try {
      await api("DELETE", "/api/items/" + id);
      ok++;
    } catch (err) {
      toast(err.message, true);
    }
  }
  state.selected.clear();
  toast(`已删除 ${ok} 件物品`);
  await refreshAll();
});

// 登录 / 注册提交
$("auth-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const username = $("auth-username").value.trim();
  const password = $("auth-password").value;
  const submitBtn = $("auth-submit");
  submitBtn.disabled = true;
  try {
    const body = authMode === "register"
      ? { username, password, confirm: $("auth-confirm").value }
      : { username, password };
    const data = await api("POST", "/api/auth/" + authMode, body);
    saveAuth(data.token, data.username);
    showApp();
    await refreshAll();
    applyRouteParams();
  } catch (err) {
    authError(err.message);
  } finally {
    submitBtn.disabled = false;
  }
});

// 详情弹窗底部操作
$("detail-edit").addEventListener("click", () => {
  if (!detailId) return;
  closeModal("detail-modal");
  openEditItem(detailId);
});
$("detail-qr").addEventListener("click", () => { if (detailId) openItemQR(detailId); });

// 位置管理独立页面切换
function openLocPage() {
  state.page = "loc";
  renderLocationList();
  $("loc-page").classList.remove("hidden");
  $("toolbar").classList.add("hidden");
  $("gallery").classList.add("hidden");
  $("bulk-bar").classList.add("hidden");
}
function closeLocPage() {
  state.page = "gallery";
  $("loc-page").classList.add("hidden");
  $("toolbar").classList.remove("hidden");
  $("gallery").classList.remove("hidden");
  $("bulk-bar").classList.toggle("hidden", !state.selectMode);
}
$("btn-manage-locations").addEventListener("click", openLocPage);
$("btn-back-gallery").addEventListener("click", closeLocPage);

// 筛选位置的布局图预览
$("btn-view-loc-image").addEventListener("click", () => {
  const loc = currentFilterLocation();
  if (loc && loc.imageFile) { openLocImage(loc.id); return; }
  toast("该位置还没有上传布局图", true);
});

document.querySelectorAll("[data-close]").forEach((btn) => {
  btn.addEventListener("click", () => {
    if (btn.dataset.close === "scan-modal") closeScan();
    else closeModal(btn.dataset.close);
  });
});

$("btn-scan").addEventListener("click", () => startScan((v) => {
  $("item-barcode").value = v;
  toast("已识别条码：" + v);
}));
$("btn-scan-search").addEventListener("click", () => startScan(handleScanValue));

bindLocationInput("item-location", "item-location-id");

// 图片预览
$("item-image").addEventListener("change", (e) => {
  const file = e.target.files && e.target.files[0];
  const preview = $("image-preview");
  if (file) {
    preview.src = URL.createObjectURL(file);
    preview.classList.remove("hidden");
  } else {
    preview.classList.add("hidden");
  }
});

// 位置布局图上传 / 更换
$("loc-image-input").addEventListener("change", async (e) => {
  const file = e.target.files && e.target.files[0];
  const id = state.pendingLocImage;
  e.target.value = "";
  if (!file || !id) return;
  const fd = new FormData();
  fd.append("image", file);
  try {
    await api("POST", "/api/locations/" + id + "/image", fd, true);
    toast("布局图已上传");
    await refreshAll();
  } catch (err) {
    toast(err.message, true);
  }
});

// ---------- 批量导入 ----------

function csvCell(v) {
  v = String(v ?? "");
  return /[",\n\r]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v;
}

function downloadTemplate() {
  const rows = [
    ["名称", "位置", "标签", "条码", "数量", "备注"],
    ["客厅路由器", "客厅 / 弱电箱", "数码, 备用", "6901234567890", "1", "主路由，备注示例"],
  ];
  const csv = "\ufeff" + rows.map((r) => r.map(csvCell).join(",")).join("\r\n");
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = "物品导入模板.csv";
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(a.href);
}

function showImportResult(res) {
  const box = $("import-result");
  box.classList.remove("hidden");
  let html = `<div class="import-summary">成功导入 <strong>${res.added}</strong> 件`;
  if (res.failed > 0) html += `，失败 <strong>${res.failed}</strong> 件`;
  html += "</div>";
  if (res.errors && res.errors.length) {
    html += "<ul class='import-errors'>" +
      res.errors.map((e) => `<li>第 ${e.row} 行「${esc(e.name)}」：${esc(e.reason)}</li>`).join("") +
      "</ul>";
  }
  box.innerHTML = html;
}

$("btn-import").addEventListener("click", () => {
  $("import-file").value = "";
  $("import-file-label").textContent = "选择 CSV 文件";
  $("import-result").classList.add("hidden");
  openModal("import-modal");
});

$("btn-template").addEventListener("click", downloadTemplate);

$("import-file").addEventListener("change", (e) => {
  const file = e.target.files && e.target.files[0];
  $("import-file-label").textContent = file ? file.name : "选择 CSV 文件";
  $("import-result").classList.add("hidden");
});

$("import-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const file = $("import-file").files && $("import-file").files[0];
  if (!file) { toast("请先选择 CSV 文件", true); return; }
  const fd = new FormData();
  fd.append("file", file);
  const submitBtn = $("import-submit");
  submitBtn.disabled = true;
  try {
    const res = await api("POST", "/api/items/import", fd, true);
    showImportResult(res);
    await refreshAll();
  } catch (err) {
    toast(err.message, true);
  } finally {
    submitBtn.disabled = false;
  }
});

// 物品提交
$("item-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const form = e.target;
  const fd = new FormData(form);
  const submitBtn = $("item-submit");
  submitBtn.disabled = true;
  try {
    if (state.editingId) {
      await api("PUT", "/api/items/" + state.editingId, fd, true);
      toast("物品已更新");
    } else {
      await api("POST", "/api/items", fd, true);
      toast("物品已添加");
    }
    closeModal("item-modal");
    await refreshAll();
  } catch (err) {
    toast(err.message, true);
  } finally {
    submitBtn.disabled = false;
  }
});

// 位置新增（根位置或指定父位置的子位置）
$("location-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const name = e.target.name.value.trim();
  if (!name) return;
  try {
    await api("POST", "/api/locations", { name, parentId: state.pendingParentId });
    e.target.reset();
    state.pendingParentId = "";
    $("location-name-input").placeholder = "根位置名称，如：主卧";
    toast("位置已添加");
    await refreshAll();
  } catch (err) {
    toast(err.message, true);
  }
});

// 位置树：展开/收起、添加子位置、删除（事件委托）
$("location-list").addEventListener("click", async (e) => {
  const toggleBtn = e.target.closest("[data-toggle]");
  if (toggleBtn) {
    const id = toggleBtn.dataset.toggle;
    if (state.expanded.has(id)) state.expanded.delete(id);
    else state.expanded.add(id);
    renderLocationList();
    return;
  }

  const childBtn = e.target.closest("[data-add-child]");
  if (childBtn) {
    const id = childBtn.dataset.addChild;
    const loc = state.byId[id];
    state.pendingParentId = id;
    $("location-name-input").placeholder = `在「${loc ? loc.name : ""}」下添加子位置`;
    $("location-name-input").focus();
    return;
  }

  const imageBtn = e.target.closest("[data-loc-image]");
  if (imageBtn) {
    state.pendingLocImage = imageBtn.dataset.locImage;
    $("loc-image-input").click();
    return;
  }

  const qrLocBtn = e.target.closest("[data-qr-loc]");
  if (qrLocBtn) { openLocQR(qrLocBtn.dataset.qrLoc); return; }

  const thumb = e.target.closest("[data-preview-loc]");
  if (thumb) { openLocImage(thumb.dataset.previewLoc); return; }

  const delBtn = e.target.closest("[data-del-loc]");
  if (!delBtn || delBtn.disabled) return;
  const id = delBtn.dataset.delLoc;
  const loc = state.byId[id];
  if (!confirm(`确定删除位置「${loc ? loc.name : ""}」吗？`)) return;
  try {
    await api("DELETE", "/api/locations/" + id);
    toast("位置已删除");
    await refreshAll();
  } catch (err) {
    toast(err.message, true);
  }
});

// 卡片操作（事件委托）：按钮走对应动作，点击卡片其余区域打开详情
$("gallery").addEventListener("click", async (e) => {
  if (state.selectMode) {
    const card = e.target.closest("[data-id]");
    if (card) toggleSelect(card.dataset.id);
    return;
  }
  const qrBtn = e.target.closest("[data-qr-item]");
  if (qrBtn) { openItemQR(qrBtn.dataset.qrItem); return; }
  const editBtn = e.target.closest("[data-edit]");
  const delBtn = e.target.closest("[data-del]");
  if (editBtn) { openEditItem(editBtn.dataset.edit); return; }
  if (delBtn) {
    const id = delBtn.dataset.del;
    const it = state.items.find((x) => x.id === id);
    if (!confirm(`确定删除物品「${it ? it.name : ""}」吗？图片将一并删除。`)) return;
    try {
      await api("DELETE", "/api/items/" + id);
      toast("物品已删除");
      await refreshAll();
    } catch (err) {
      toast(err.message, true);
    }
    return;
  }
  const card = e.target.closest("[data-id]");
  if (card) openDetail(card.dataset.id);
});

// 识别链接参数：?item=<id> 查看物品二维码，?loc=<id> 筛选到该位置。
function applyRouteParams() {
  const params = new URLSearchParams(location.search);
  const locId = params.get("loc");
  const itemId = params.get("item");
  if (locId && state.byId[locId]) applyLocationFilter(locId);
  if (itemId) openDetail(itemId);
}

// 启动：先校验登录态，已登录再加载数据
bootAuth()
  .then(async (ok) => {
    if (!ok) return;
    await refreshAll();
    applyRouteParams();
  })
  .catch((err) => toast("初始化失败：" + err.message, true));
