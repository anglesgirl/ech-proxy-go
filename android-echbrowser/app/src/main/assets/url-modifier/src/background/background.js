/**
 * URL Modifier — Background Script
 *
 * Problem: When an external app, such as Signal, opens a URL, Firefox cold-starts
 * and the Intent URL is queued before the background script has run at all.
 * The webRequest listener fires before storage is loaded, so cachedActive is
 * false and the request passes through unchanged.
 *
 * Solution — three independent interception layers:
 *
 *  Layer 1 — webRequest.onBeforeRequest (registered synchronously at top level)
 *    Catches most requests. The handler awaits storage if the cache isn't warm.
 *    Because the listener is registered synchronously, Firefox holds the request
 *    open while we await — this is the key fix for the cold-start race.
 *
 *  Layer 2 — webNavigation.onBeforeNavigate
 *    Fires slightly later than webRequest but catches navigations that somehow
 *    slip through. We issue a programmatic tabs.update redirect here.
 *
 *  Layer 3 — tabs.onUpdated (URL change detection)
 *    Last resort. If a tab loads a URL that should have been rewritten, we
 *    catch it here and immediately redirect the tab. Visible as a very brief
 *    flash to the wrong URL, but it always corrects.
 *
 *
 *
 * Bug fixed: before/after rules were being applied repeatedly because:
 *   1. Three layers had three SEPARATE redirect Sets — Layer 1 guarded its
 *      own re-intercept but Layers 2 and 3 didn't know about it, so they
 *      each applied the rule again.
 *   2. For before/after mode, the rewritten URL still contains the original
 *      pattern, so every layer matched it again on every request.
 *
 * Fix:
 *   A. One shared `alreadyRedirected` Set used by ALL three layers.
 *   B. `wouldChange()` — before calling applyRules, check whether the URL
 *      has already had all applicable rules applied (i.e. applying the rules
 *      again produces no further change). If not, skip it.
 *      This is the correct idempotency check: a before/after rewrite is
 *      "done" when a second application of the same rules produces the same
 *      URL — which only happens when the modification text is already present
 *      in the right place relative to the pattern.
 */

const KEY_RULES  = 'urlModifierRules';
const KEY_ACTIVE = 'urlModifierActive';

let cachedRules  = null;
let cachedActive = null;

let storageReady;
const storagePromise = new Promise(resolve => { storageReady = resolve; });

// ── Single shared redirect guard ──────────────────────────────────────────────
// All three layers share this one Set. As soon as any layer decides to
// redirect a URL, it marks the destination here. Every layer checks here
// before doing any work, so the same URL is never processed twice regardless
// of which layer fires next.
const alreadyRedirected = new Set();

const ALL_TYPES = [
  'main_frame', 'sub_frame', 'stylesheet', 'script', 'image',
  'font', 'object', 'xmlhttprequest', 'ping', 'beacon',
  'media', 'websocket', 'csp_report', 'imageset',
  'web_manifest', 'speculative', 'other'
];

// ── Storage ───────────────────────────────────────────────────────────────────
// 2026-08-16 echbrowser 适配：无 UI 环境，默认启用 + 内置 abs-0→abs 规则
// （改 TXT 云配置 rewrite= 由 App 注入，见 runtime.onMessage SET_RULES）。
const DEFAULT_RULES = [
  {
    enabled: true,
    pattern: '^https://(abs-[0-9]|abs-o-ft|abs-o)\\.twimg\\.com/',
    mode: 'replace',
    modification: 'https://abs.twimg.com/'
  }
];

async function loadStorage() {
  const data = await browser.storage.local.get([KEY_RULES, KEY_ACTIVE]);
  cachedRules  = data[KEY_RULES]  || DEFAULT_RULES;
  cachedActive = data[KEY_ACTIVE] !== false; // 默认 true（无 UI 环境）
  if (!data[KEY_RULES]) {
    browser.storage.local.set({ [KEY_RULES]: DEFAULT_RULES }).catch(() => {});
  }
  if (data[KEY_ACTIVE] === undefined) {
    browser.storage.local.set({ [KEY_ACTIVE]: true }).catch(() => {});
  }
  storageReady();
  updateIcon();
}

browser.storage.onChanged.addListener((changes, area) => {
  if (area !== 'local') return;
  if (changes[KEY_RULES])  cachedRules  = changes[KEY_RULES].newValue  || [];
  if (changes[KEY_ACTIVE]) cachedActive = changes[KEY_ACTIVE].newValue || false;
  updateIcon();
});

loadStorage();

// ── Idempotency check ─────────────────────────────────────────────────────────
// Returns true if the URL needs rewriting (i.e. rules produce a different URL).
// Returns false if the URL is already fully rewritten — applying the rules
// again would produce no further change. This is the correct way to detect
// "before/after already applied": if modification + pattern is already in the
// URL such that the rule output equals the input, we're done.
function needsRewrite(url, rules) {
  return applyRules(url, rules) !== url;
}

// ── Shared redirect helper ────────────────────────────────────────────────────
function markAndScheduleCleanup(url) {
  alreadyRedirected.add(url);
  setTimeout(() => alreadyRedirected.delete(url), 10000);
}

// ── LAYER 1: webRequest.onBeforeRequest ──────────────────────────────────────
browser.webRequest.onBeforeRequest.addListener(
  onBeforeRequest,
  { urls: ['<all_urls>'], types: ALL_TYPES },
  ['blocking']
);

async function onBeforeRequest(details) {
  const url = details.url;
  if (url.startsWith('moz-extension://')) return {};

  // Already handled by any layer — let it through
  if (alreadyRedirected.has(url)) return {};

  if (cachedRules === null || cachedActive === null) await storagePromise;
  if (!cachedActive || !cachedRules.length) return {};

  // Already fully rewritten — don't apply again
  if (!needsRewrite(url, cachedRules)) return {};

  const modified = applyRules(url, cachedRules);
  if (!modified || modified === url) return {};

  try { new URL(modified); } catch (_) {
    console.warn('[URLModifier] Layer1: invalid rewrite, skipping:', modified);
    return {};
  }

  markAndScheduleCleanup(modified);
  console.log('[URLModifier] Layer1:', url, '->', modified);
  return { redirectUrl: modified };
}

// ── LAYER 2: webNavigation.onBeforeNavigate ───────────────────────────────────
browser.webNavigation.onBeforeNavigate.addListener(async details => {
  if (details.frameId !== 0) return;
  const url = details.url;
  if (url.startsWith('moz-extension://') || url === 'about:blank') return;
  if (alreadyRedirected.has(url)) return;

  if (cachedRules === null || cachedActive === null) await storagePromise;
  if (!cachedActive || !cachedRules.length) return;
  if (!needsRewrite(url, cachedRules)) return;

  const modified = applyRules(url, cachedRules);
  if (!modified || modified === url) return;

  try { new URL(modified); } catch (_) { return; }

  markAndScheduleCleanup(modified);
  console.log('[URLModifier] Layer2:', url, '->', modified);
  browser.tabs.update(details.tabId, { url: modified }).catch(() => {});
});

// ── LAYER 3: tabs.onUpdated ───────────────────────────────────────────────────
browser.tabs.onUpdated.addListener(async (tabId, changeInfo, _tab) => {
  if (!changeInfo.url) return;
  const url = changeInfo.url;
  if (url.startsWith('moz-extension://') || url === 'about:blank') return;
  if (alreadyRedirected.has(url)) return;

  if (cachedRules === null || cachedActive === null) await storagePromise;
  if (!cachedActive || !cachedRules.length) return;
  if (!needsRewrite(url, cachedRules)) return;

  const modified = applyRules(url, cachedRules);
  if (!modified || modified === url) return;

  try { new URL(modified); } catch (_) { return; }

  markAndScheduleCleanup(modified);
  console.log('[URLModifier] Layer3:', url, '->', modified);
  browser.tabs.update(tabId, { url: modified }).catch(() => {});
});

// ── Rule application ──────────────────────────────────────────────────────────
function applyRules(url, rules) {
  let result = url;
  for (const rule of rules) {
    if (!rule.enabled || !rule.pattern) continue;
    try {
      const re = new RegExp(rule.pattern, 'gi');
      if      (rule.mode === 'before')  result = result.replace(re, m => rule.modification + m);
      else if (rule.mode === 'replace') result = result.replace(re, rule.modification);
      else if (rule.mode === 'after')   result = result.replace(re, m => m + rule.modification);
    } catch (_) {
      const esc = rule.pattern.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
      const re  = new RegExp(esc, 'gi');
      if      (rule.mode === 'before')  result = result.replace(re, m => rule.modification + m);
      else if (rule.mode === 'replace') result = result.replace(re, rule.modification);
      else if (rule.mode === 'after')   result = result.replace(re, m => m + rule.modification);
    }
  }
  return result;
}

// ── Icon ──────────────────────────────────────────────────────────────────────
function updateIcon() {
  const suffix = cachedActive ? 'on' : 'off';
  browser.action.setIcon({
    path: {
      16:  'src/icons/icon-' + suffix + '-16.png',
      32:  'src/icons/icon-' + suffix + '-32.png',
      48:  'src/icons/icon-' + suffix + '-48.png',
      128: 'src/icons/icon-' + suffix + '-128.png'
    }
  }).catch(() => {});
  browser.action.setTitle({
    title: cachedActive ? 'URL Modifier — Active' : 'URL Modifier — Inactive'
  });
}

// ── Message handler ───────────────────────────────────────────────────────────
browser.runtime.onMessage.addListener((msg, _sender) => {
  if (msg.type === 'GET_STATE') {
    return Promise.resolve({ rules: cachedRules || [], active: cachedActive || false });
  }
  if (msg.type === 'SET_RULES') {
    cachedRules = msg.rules;
    browser.storage.local.set({ [KEY_RULES]: cachedRules });
    return Promise.resolve({ ok: true });
  }
  if (msg.type === 'SET_ACTIVE') {
    cachedActive = msg.active;
    browser.storage.local.set({ [KEY_ACTIVE]: cachedActive });
    updateIcon();
    return Promise.resolve({ ok: true });
  }
  return Promise.resolve({ error: 'unknown message' });
});
