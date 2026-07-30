import {
  applyTranslations,
  getLocale,
  initializeI18n,
  localeOptions,
  setLocale,
  t,
  tp,
} from "./i18n/index.js";

"use strict";

await initializeI18n();
applyTranslations();

const state = {
  chats: [],
  original: new Map(),
  drafts: new Map(),
  selected: new Set(),
  filter: "all",
  query: "",
  saving: false,
  sessionToken: "",
  connected: false,
  settings: {
    allow_start_new_conversations: false,
    revision: 1,
  },
};

const elements = {
  loginView: document.querySelector("#login-view"),
  appView: document.querySelector("#app-view"),
  loginForm: document.querySelector("#login-form"),
  loginButton: document.querySelector("#login-button"),
  loginError: document.querySelector("#login-error"),
  tokenInput: document.querySelector("#admin-token"),
  toggleToken: document.querySelector("#toggle-token"),
  logoutButton: document.querySelector("#logout-button"),
  refreshButton: document.querySelector("#refresh-button"),
  connectionStatus: document.querySelector("#connection-status"),
  disconnectedBanner: document.querySelector("#disconnected-banner"),
  searchInput: document.querySelector("#search-input"),
  filterTabs: [...document.querySelectorAll(".filter-tab")],
  selectVisible: document.querySelector("#select-visible"),
  bulkPreset: document.querySelector("#bulk-preset"),
  loadingState: document.querySelector("#loading-state"),
  emptyState: document.querySelector("#empty-state"),
  emptyCopy: document.querySelector("#empty-copy"),
  clearSearch: document.querySelector("#clear-search"),
  list: document.querySelector("#conversation-list"),
  resultCount: document.querySelector("#result-count"),
  readCount: document.querySelector("#read-count"),
  historyCount: document.querySelector("#history-count"),
  sendCount: document.querySelector("#send-count"),
  totalCount: document.querySelector("#total-count"),
  saveBar: document.querySelector("#save-bar"),
  pendingCount: document.querySelector("#pending-count"),
  discardButton: document.querySelector("#discard-button"),
  saveButton: document.querySelector("#save-button"),
  confirmDialog: document.querySelector("#confirm-dialog"),
  confirmCopy: document.querySelector("#confirm-copy"),
  newConversationDialog: document.querySelector("#new-conversation-dialog"),
  newConversationToggle: document.querySelector("#allow-start-new-conversations"),
  newConversationStatus: document.querySelector("#new-conversations-status"),
  globalAccessCard: document.querySelector(".global-access-card"),
  languageSelects: [...document.querySelectorAll(".language-select")],
  toast: document.querySelector("#toast"),
  liveRegion: document.querySelector("#live-region"),
};

syncLanguageSelectors();

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    credentials: "omit",
    headers: {
      Accept: "application/json",
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(state.sessionToken
        ? { "X-WhatsApp-MCP-Admin-Session": state.sessionToken }
        : {}),
      ...(options.headers || {}),
    },
  });

  if (response.status === 401 && path !== "/admin/login") {
    showLogin();
    throw new Error(t("error.sessionExpired"));
  }

  const contentType = response.headers.get("content-type") || "";
  const body = contentType.includes("application/json") ? await response.json() : await response.text();
  if (!response.ok) {
    const message = typeof body === "object" ? body.error || body.message : body;
    throw new Error(message || t("error.http", { status: response.status }));
  }
  return body;
}

function showLogin(message = "") {
  state.sessionToken = "";
  elements.appView.hidden = true;
  elements.loginView.hidden = false;
  elements.loginError.hidden = !message;
  elements.loginError.textContent = message;
  elements.tokenInput.value = "";
  window.setTimeout(() => elements.tokenInput.focus(), 20);
}

function showApp() {
  elements.loginView.hidden = true;
  elements.appView.hidden = false;
}

async function login(event) {
  event.preventDefault();
  const token = elements.tokenInput.value.trim();
  if (!token) return;

  setButtonBusy(elements.loginButton, true, t("login.submitting"));
  elements.loginError.hidden = true;
  try {
    const payload = await api("/admin/login", {
      method: "POST",
      body: JSON.stringify({ token }),
    });
    if (!payload || typeof payload.session_token !== "string" || !payload.session_token) {
      throw new Error(t("login.invalidSession"));
    }
    state.sessionToken = payload.session_token;
    elements.tokenInput.value = "";
    await loadChats();
  } catch (error) {
    elements.loginError.textContent =
      error.message === "Invalid admin token"
        ? t("login.invalidToken")
        : error.message;
    elements.loginError.hidden = false;
  } finally {
    setButtonBusy(elements.loginButton, false, t("login.submit"));
  }
}

async function logout() {
  try {
    await api("/admin/logout", { method: "POST" });
  } catch {
    // Clearing the local view is still the safest outcome if the request fails.
  }
  resetState();
  showLogin();
}

async function loadChats({ preserveDrafts = false } = {}) {
  elements.loadingState.hidden = false;
  elements.emptyState.hidden = true;
  elements.list.hidden = true;
  elements.refreshButton.disabled = true;

  try {
    const payload = await api("/api/admin/chats");
    showApp();
    state.chats = Array.isArray(payload.chats) ? payload.chats : [];
    state.settings = {
      allow_start_new_conversations: Boolean(payload.settings?.allow_start_new_conversations),
      revision: Number.isInteger(payload.settings?.revision) ? payload.settings.revision : 1,
    };
    state.connected = Boolean(payload.connected);

    if (!preserveDrafts) {
      state.original = new Map(state.chats.map((chat) => [chat.jid, permissionSnapshot(chat)]));
      state.drafts = new Map(state.chats.map((chat) => [chat.jid, { ...chat }]));
      state.selected.clear();
    } else {
      const previousDrafts = state.drafts;
      const previousOriginal = state.original;
      const nextOriginal = new Map();
      const nextDrafts = new Map();
      for (const chat of state.chats) {
        const previous = previousDrafts.get(chat.jid);
        const previousBaseline = previousOriginal.get(chat.jid);
        const hasPendingChange =
          previous &&
          previousBaseline &&
          !permissionSnapshotsEqual(permissionSnapshot(previous), previousBaseline);
        if (hasPendingChange) {
          nextOriginal.set(chat.jid, previousBaseline);
          nextDrafts.set(chat.jid, {
            ...chat,
            ...permissionSnapshot(previous),
            permissions_revision: previous.permissions_revision || 0,
          });
        } else {
          nextOriginal.set(chat.jid, permissionSnapshot(chat));
          nextDrafts.set(chat.jid, { ...chat });
        }
      }
      state.original = nextOriginal;
      state.drafts = nextDrafts;
    }

    updateConnection(state.connected);
    render();
  } catch (error) {
    if (!elements.loginView.hidden) return;
    showToast(t("error.load", { message: error.message }), true);
    elements.emptyCopy.textContent = t("empty.loadError");
    elements.emptyState.hidden = false;
  } finally {
    elements.loadingState.hidden = true;
    elements.refreshButton.disabled = false;
  }
}

function permissionSnapshot(chat) {
  return {
    read_new: Boolean(chat.read_new),
    read_history: Boolean(chat.read_history),
    can_send: Boolean(chat.can_send),
  };
}

function permissionSnapshotsEqual(left, right) {
  return (
    left.read_new === right.read_new &&
    left.read_history === right.read_history &&
    left.can_send === right.can_send
  );
}

function resetState() {
  state.chats = [];
  state.original.clear();
  state.drafts.clear();
  state.selected.clear();
  state.filter = "all";
  state.query = "";
  state.sessionToken = "";
  state.connected = false;
  state.settings = {
    allow_start_new_conversations: false,
    revision: 1,
  };
  elements.searchInput.value = "";
}

function render() {
  const visible = getVisibleChats();
  renderSummary();
  renderRows(visible);
  renderPending();
  renderSelection(visible);
  renderGlobalSettings();

  elements.resultCount.textContent =
    visible.length === state.chats.length
      ? tp("result.chats", state.chats.length)
      : t("result.filtered", { visible: visible.length, total: state.chats.length });

  const hasChats = visible.length > 0;
  elements.list.hidden = !hasChats;
  elements.emptyState.hidden = hasChats;
  if (!hasChats) {
    elements.emptyCopy.textContent =
      state.query || state.filter !== "all"
        ? t("empty.filters")
        : t("empty.sync");
  }
}

function renderGlobalSettings() {
  const enabled = Boolean(state.settings.allow_start_new_conversations);
  elements.newConversationToggle.checked = enabled;
  elements.newConversationStatus.textContent = enabled ? t("global.allowed") : t("global.disabled");
  elements.globalAccessCard.classList.toggle("enabled", enabled);
}

function getVisibleChats() {
  const query = normalizeText(state.query);
  return state.chats.filter((base) => {
    const chat = state.drafts.get(base.jid) || base;
    const hasAccess = chat.read_new || chat.read_history || chat.can_send;
    const matchesFilter =
      state.filter === "all" ||
      (state.filter === "allowed" && hasAccess) ||
      (state.filter === "blocked" && !hasAccess) ||
      (state.filter === "groups" && chat.is_group) ||
      (state.filter === "people" && !chat.is_group);
    const matchesQuery =
      !query || normalizeText(`${chat.name || ""} ${chat.jid || ""}`).includes(query);
    return matchesFilter && matchesQuery;
  });
}

function renderSummary() {
  const drafts = [...state.drafts.values()];
  elements.readCount.textContent = String(drafts.filter((chat) => chat.read_new).length);
  elements.historyCount.textContent = String(drafts.filter((chat) => chat.read_history).length);
  elements.sendCount.textContent = String(drafts.filter((chat) => chat.can_send).length);
  elements.totalCount.textContent = String(drafts.length);
}

function renderRows(chats) {
  elements.list.innerHTML = chats
    .map((base) => {
      const chat = state.drafts.get(base.jid) || base;
      const selected = state.selected.has(chat.jid);
      const displayName = chat.name || friendlyJID(chat.jid);
      const activity = formatActivity(chat.last_message_time);
      const type = chat.is_group ? t("chat.type.group") : t("chat.type.person");
      const readNewHint = chat.read_new
        ? t("chat.since", { date: formatUnix(chat.read_new_since_unix) })
        : t("chat.disabled");
      const historyHint = chat.read_history
        ? t("chat.through", { date: formatUnix(chat.read_history_through_unix) })
        : t("chat.disabled");
      const badge = accessBadge(chat);

      return `
        <article class="conversation-row${selected ? " selected" : ""}" role="listitem" data-jid="${escapeHTML(chat.jid)}">
          <div class="conversation-identity">
            <input
              class="row-select"
              type="checkbox"
              aria-label="${escapeHTML(t("chat.select", { name: displayName }))}"
              ${selected ? "checked" : ""}
            />
            <span class="avatar${chat.is_group ? " group" : ""}" aria-hidden="true">${escapeHTML(initials(displayName))}</span>
            <span class="conversation-meta">
              <span class="conversation-name" title="${escapeHTML(displayName)}">${escapeHTML(displayName)}</span>
              <span class="conversation-details">
                <span title="${escapeHTML(chat.jid)}">${escapeHTML(friendlyJID(chat.jid))}</span>
                <span>${type}</span>
                ${activity ? `<span>${escapeHTML(activity)}</span>` : ""}
              </span>
            </span>
          </div>
          ${permissionSwitch(chat, "read_new", t("chat.readNew"), readNewHint)}
          ${permissionSwitch(chat, "read_history", t("chat.readHistory"), historyHint)}
          ${permissionSwitch(chat, "can_send", t("chat.send"), chat.can_send ? t("chat.allowed") : t("chat.disabled"))}
          <span class="state-badge ${badge.className}">${badge.label}</span>
        </article>
      `;
    })
    .join("");
}

function permissionSwitch(chat, permission, label, hint) {
  const displayName = chat.name || friendlyJID(chat.jid);
  return `
    <div class="permission-cell" data-permission="${permission}">
      <span class="permission-mobile-label">${escapeHTML(label)}</span>
      <label class="switch">
        <span class="sr-only">${escapeHTML(t("chat.permissionFor", { permission: label, name: displayName }))}</span>
        <input
          class="permission-toggle"
          type="checkbox"
          data-permission="${permission}"
          ${chat[permission] ? "checked" : ""}
        />
        <span class="switch-track" aria-hidden="true"></span>
      </label>
      <small>${escapeHTML(hint)}</small>
    </div>
  `;
}

function accessBadge(chat) {
  const { read_new: readNew, read_history: history, can_send: send } = chat;
  if (!readNew && !history && !send) return { label: t("badge.blocked"), className: "" };
  if (!readNew && !history && send) return { label: t("badge.sendOnly"), className: "send-only" };
  if (readNew && history && send) return { label: t("badge.full"), className: "allowed" };
  if (readNew && history) return { label: t("badge.readAll"), className: "allowed" };
  if (readNew && !send) return { label: t("badge.newOnly"), className: "allowed" };
  if (history && !send) return { label: t("badge.historyOnly"), className: "allowed" };
  return { label: t("badge.custom"), className: "allowed" };
}

function renderPending() {
  const changed = changedChats();
  const count = changed.length;
  elements.saveBar.hidden = count === 0;
  elements.pendingCount.textContent = tp("save.pending", count);
  elements.saveButton.disabled = state.saving || count === 0;
  elements.discardButton.disabled = state.saving || count === 0;
}

function renderSelection(visible) {
  const visibleIDs = visible.map((chat) => chat.jid);
  const selectedVisible = visibleIDs.filter((jid) => state.selected.has(jid)).length;
  elements.selectVisible.checked = visibleIDs.length > 0 && selectedVisible === visibleIDs.length;
  elements.selectVisible.indeterminate = selectedVisible > 0 && selectedVisible < visibleIDs.length;
  elements.bulkPreset.disabled = state.selected.size === 0;
}

function changedChats() {
  const changes = [];
  for (const [jid, draft] of state.drafts) {
    const original = state.original.get(jid);
    if (!original) continue;
    if (
      draft.read_new !== original.read_new ||
      draft.read_history !== original.read_history ||
      draft.can_send !== original.can_send
    ) {
      changes.push(draft);
    }
  }
  return changes;
}

function updatePermission(jid, permission, enabled) {
  const draft = state.drafts.get(jid);
  if (!draft || !["read_new", "read_history", "can_send"].includes(permission)) return;
  draft[permission] = enabled;
  render();
}

function toggleSelection(jid, selected) {
  if (selected) state.selected.add(jid);
  else state.selected.delete(jid);
  render();
}

function selectAllVisible(selected) {
  for (const chat of getVisibleChats()) {
    if (selected) state.selected.add(chat.jid);
    else state.selected.delete(chat.jid);
  }
  render();
}

function applyBulkPreset(preset) {
  const values = {
    block: { read_new: false, read_history: false, can_send: false },
    new: { read_new: true, read_history: false, can_send: false },
    read: { read_new: true, read_history: true, can_send: false },
    all: { read_new: true, read_history: true, can_send: true },
  }[preset];
  if (!values) return;

  for (const jid of state.selected) {
    const draft = state.drafts.get(jid);
    if (draft) Object.assign(draft, values);
  }
  elements.bulkPreset.value = "";
  render();
}

function discardChanges() {
  state.original = new Map();
  state.drafts = new Map();
  for (const base of state.chats) {
    state.original.set(base.jid, permissionSnapshot(base));
    state.drafts.set(base.jid, { ...base });
  }
  showToast(t("save.discarded"));
  render();
}

async function saveChanges() {
  const changed = changedChats();
  if (changed.length === 0 || state.saving) return;

  const expansions = changed.filter((chat) => {
    const original = state.original.get(chat.jid);
    return (
      (!original.read_new && chat.read_new) ||
      (!original.read_history && chat.read_history) ||
      (!original.can_send && chat.can_send)
    );
  });

  if (expansions.length > 0) {
    const confirmed = await confirmExpansion(expansions);
    if (!confirmed) return;
  }

  state.saving = true;
  setButtonBusy(elements.saveButton, true, t("save.saving"));
  renderPending();
  try {
    await api("/api/admin/permissions", {
      method: "PUT",
      body: JSON.stringify({
        updates: changed.map((chat) => ({
          chat_jid: chat.jid,
          expected_revision: chat.permissions_revision || 0,
          read_new: Boolean(chat.read_new),
          read_history: Boolean(chat.read_history),
          can_send: Boolean(chat.can_send),
        })),
      }),
    });
    await loadChats();
    showToast(t("save.success"));
    elements.liveRegion.textContent = t("save.live");
  } catch (error) {
    showToast(t("error.save", { message: error.message }), true);
  } finally {
    state.saving = false;
    setButtonBusy(elements.saveButton, false, t("save.button"));
    renderPending();
  }
}

function confirmExpansion(expansions) {
  const permissions = {
    read_new: expansions.filter((chat) => !state.original.get(chat.jid).read_new && chat.read_new).length,
    read_history: expansions.filter(
      (chat) => !state.original.get(chat.jid).read_history && chat.read_history,
    ).length,
    can_send: expansions.filter((chat) => !state.original.get(chat.jid).can_send && chat.can_send).length,
  };
  const details = [
    permissions.read_new ? t("confirm.readNew", { count: permissions.read_new }) : "",
    permissions.read_history ? t("confirm.history", { count: permissions.read_history }) : "",
    permissions.can_send ? t("confirm.send", { count: permissions.can_send }) : "",
  ]
    .filter(Boolean)
    .join(", ");
  elements.confirmCopy.textContent = t("dialog.expandCopy", {
    chats: tp("confirm.chats", expansions.length),
    details,
  });

  return new Promise((resolve) => {
    const onClose = () => {
      elements.confirmDialog.removeEventListener("close", onClose);
      resolve(elements.confirmDialog.returnValue === "confirm");
    };
    elements.confirmDialog.addEventListener("close", onClose);
    elements.confirmDialog.showModal();
  });
}

function confirmNewConversations() {
  return new Promise((resolve) => {
    const onClose = () => {
      elements.newConversationDialog.removeEventListener("close", onClose);
      resolve(elements.newConversationDialog.returnValue === "confirm");
    };
    elements.newConversationDialog.addEventListener("close", onClose);
    elements.newConversationDialog.showModal();
  });
}

async function updateNewConversationSetting(event) {
  const desired = event.target.checked;
  const previous = Boolean(state.settings.allow_start_new_conversations);
  if (desired === previous) return;

  if (desired && !(await confirmNewConversations())) {
    renderGlobalSettings();
    return;
  }

  elements.newConversationToggle.disabled = true;
  try {
    const settings = await api("/api/admin/settings", {
      method: "PUT",
      body: JSON.stringify({
        allow_start_new_conversations: desired,
        expected_revision: state.settings.revision,
      }),
    });
    state.settings = {
      allow_start_new_conversations: Boolean(settings.allow_start_new_conversations),
      revision: settings.revision,
    };
    renderGlobalSettings();
    showToast(
      desired ? t("global.enabledToast") : t("global.disabledToast"),
    );
    elements.liveRegion.textContent = desired
      ? t("global.enabledLive")
      : t("global.disabledLive");
  } catch (error) {
    renderGlobalSettings();
    showToast(t("error.update", { message: error.message }), true);
  } finally {
    elements.newConversationToggle.disabled = false;
  }
}

function updateConnection(connected) {
  elements.connectionStatus.classList.toggle("connected", connected);
  elements.connectionStatus.classList.toggle("disconnected", !connected);
  const label = connected
    ? t("connection.connected")
    : t("connection.disconnected");
  elements.connectionStatus.querySelector("span:last-child").textContent = label;
  elements.connectionStatus.setAttribute("aria-label", label);
  elements.disconnectedBanner.hidden = connected;
}

function setButtonBusy(button, busy, label) {
  button.disabled = busy;
  const labelElement = button.querySelector("span");
  if (labelElement) labelElement.textContent = label;
  else button.textContent = label;
}

let toastTimer;
function showToast(message, isError = false) {
  window.clearTimeout(toastTimer);
  elements.toast.textContent = message;
  elements.toast.classList.toggle("error", isError);
  elements.toast.hidden = false;
  toastTimer = window.setTimeout(() => {
    elements.toast.hidden = true;
  }, 4200);
}

function formatUnix(value) {
  if (!Number.isFinite(value)) return t("date.now");
  return new Intl.DateTimeFormat(getLocale(), {
    day: "2-digit",
    month: "short",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value * 1000));
}

function formatActivity(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const now = new Date();
  const sameDay = date.toDateString() === now.toDateString();
  if (sameDay) {
    const time = new Intl.DateTimeFormat(getLocale(), {
      hour: "2-digit",
      minute: "2-digit",
    }).format(date);
    return t("date.today", { time });
  }
  return new Intl.DateTimeFormat(getLocale(), { day: "2-digit", month: "short" }).format(date);
}

function initials(value) {
  const words = String(value || "?")
    .trim()
    .split(/\s+/)
    .filter(Boolean);
  if (words.length === 0) return "?";
  return words
    .slice(0, 2)
    .map((word) => [...word][0] || "")
    .join("")
    .toLocaleUpperCase(getLocale());
}

function friendlyJID(jid) {
  const [user, server] = String(jid || "").split("@", 2);
  if (server === "g.us") return t("jid.group", { user });
  if (server === "lid") return `${user} · LID`;
  return user || jid;
}

function normalizeText(value) {
  return String(value || "")
    .normalize("NFD")
    .replace(/\p{Diacritic}/gu, "")
    .toLocaleLowerCase(getLocale());
}

function syncLanguageSelectors() {
  const available = new Set(localeOptions());
  for (const select of elements.languageSelects) {
    select.value = available.has(getLocale()) ? getLocale() : "pt-BR";
  }
}

function updateTokenVisibilityLabel() {
  const showing = elements.tokenInput.type === "text";
  elements.toggleToken.setAttribute(
    "aria-label",
    showing ? t("login.hideToken") : t("login.showToken"),
  );
}

function changeLanguage(event) {
  setLocale(event.target.value);
  applyTranslations();
  syncLanguageSelectors();
  updateTokenVisibilityLabel();
  updateConnection(state.connected);
  render();
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

elements.loginForm.addEventListener("submit", login);
elements.toggleToken.addEventListener("click", () => {
  const showing = elements.tokenInput.type === "text";
  elements.tokenInput.type = showing ? "password" : "text";
  updateTokenVisibilityLabel();
});
elements.languageSelects.forEach((select) => select.addEventListener("change", changeLanguage));
elements.logoutButton.addEventListener("click", logout);
elements.refreshButton.addEventListener("click", () => loadChats({ preserveDrafts: changedChats().length > 0 }));
elements.searchInput.addEventListener("input", (event) => {
  state.query = event.target.value;
  render();
});
elements.filterTabs.forEach((button) => {
  button.addEventListener("click", () => {
    state.filter = button.dataset.filter;
    elements.filterTabs.forEach((tab) => tab.classList.toggle("active", tab === button));
    render();
  });
});
elements.list.addEventListener("change", (event) => {
  const row = event.target.closest(".conversation-row");
  if (!row) return;
  if (event.target.classList.contains("permission-toggle")) {
    updatePermission(row.dataset.jid, event.target.dataset.permission, event.target.checked);
  } else if (event.target.classList.contains("row-select")) {
    toggleSelection(row.dataset.jid, event.target.checked);
  }
});
elements.selectVisible.addEventListener("change", (event) => selectAllVisible(event.target.checked));
elements.bulkPreset.addEventListener("change", (event) => applyBulkPreset(event.target.value));
elements.clearSearch.addEventListener("click", () => {
  state.query = "";
  state.filter = "all";
  elements.searchInput.value = "";
  elements.filterTabs.forEach((tab) => tab.classList.toggle("active", tab.dataset.filter === "all"));
  render();
});
elements.newConversationToggle.addEventListener("change", updateNewConversationSetting);
elements.discardButton.addEventListener("click", discardChanges);
elements.saveButton.addEventListener("click", saveChanges);

loadChats();
