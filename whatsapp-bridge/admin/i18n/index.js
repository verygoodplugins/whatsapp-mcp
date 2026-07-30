"use strict";

const supportedLocales = ["pt-BR", "en"];
const catalogs = new Map();
let currentLocale = "pt-BR";

export async function initializeI18n() {
  await Promise.all(
    supportedLocales.map(async (locale) => {
      const response = await fetch(new URL(`./${locale}.json`, import.meta.url), {
        credentials: "omit",
        cache: "no-store",
      });
      if (!response.ok) {
        throw new Error(`Could not load ${locale} translations`);
      }
      catalogs.set(locale, await response.json());
    }),
  );

  let saved = null;
  try {
    saved = window.localStorage.getItem("whatsapp-mcp-admin-locale");
  } catch {
    // Language persistence is optional; the panel still follows the browser.
  }
  const browserLocale = navigator.language?.toLowerCase().startsWith("en") ? "en" : "pt-BR";
  setLocale(supportedLocales.includes(saved) ? saved : browserLocale, { persist: false });
}

export function getLocale() {
  return currentLocale;
}

export function setLocale(locale, { persist = true } = {}) {
  if (!supportedLocales.includes(locale)) {
    locale = "pt-BR";
  }
  currentLocale = locale;
  document.documentElement.lang = locale;
  if (persist) {
    try {
      window.localStorage.setItem("whatsapp-mcp-admin-locale", locale);
    } catch {
      // A disabled storage API must not prevent an in-memory language switch.
    }
  }
}

export function t(key, variables = {}) {
  const catalog = catalogs.get(currentLocale) || {};
  const fallback = catalogs.get("pt-BR") || {};
  const template = catalog[key] ?? fallback[key] ?? key;
  return String(template).replace(/\{\{(\w+)\}\}/g, (_match, name) =>
    Object.prototype.hasOwnProperty.call(variables, name) ? String(variables[name]) : "",
  );
}

export function tp(baseKey, count, variables = {}) {
  const category = new Intl.PluralRules(currentLocale).select(count) === "one" ? "one" : "other";
  return t(`${baseKey}.${category}`, { count, ...variables });
}

export function applyTranslations(root = document) {
  for (const element of root.querySelectorAll("[data-i18n]")) {
    element.textContent = t(element.dataset.i18n);
  }
  for (const [attribute, datasetKey] of [
    ["placeholder", "i18nPlaceholder"],
    ["aria-label", "i18nAriaLabel"],
    ["title", "i18nTitle"],
  ]) {
    for (const element of root.querySelectorAll(`[data-${datasetKey.replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)}]`)) {
      element.setAttribute(attribute, t(element.dataset[datasetKey]));
    }
  }
  document.title = t("meta.title");
}

export function localeOptions() {
  return [...supportedLocales];
}
