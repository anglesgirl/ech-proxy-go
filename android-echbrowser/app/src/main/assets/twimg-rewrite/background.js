// twimg-rewrite —— 把 CF 上没有配置的 twimg 子域重写到 abs.twimg.com。
//
// 背景（2026-08-15 实测）：
//   x.com 前端硬编码引用 abs-0.twimg.com 加载 JS/CSS，但该域名在
//   Cloudflare 上没有任何配置（明文 SNI 打 CF 边缘握手失败 code=000）。
//   abs.twimg.com 虽然 DNS 也指向 Fastly，CF 边缘却接受它的 ECH 握手
//   （cloudflare-ech.com 证书）→ 强改强注、走 ECH 隐藏 SNI。
//   内容等价性已实测：同路径 624557 字节仅差 40 字节（Sentry release id）。
//
// 诊断通道：所有关键事件通过 browser.runtime.sendMessage 上报给 App，
// App 侧 setMessageDelegate 接收后写入 echbrowser.log（EXT 前缀），
// 用于确认 background 是否运行、监听是否注册、拦截是否触发。

// CF 上没有配置、必须重写的 twimg 子域。
// 2026-08-15 DNS 实测：abs-1/2/3、abs-vod 全部 NXDOMAIN（不存在）；
// 只有 abs-0 真实存在且被 x.com 引用。abs-o-ft（CNAME abs-o.twimg.com）
// 存在但连不通 —— 一并映射，出现即兜住。
const REWRITE_HOSTS = new Set([
  "abs-0.twimg.com",
  "abs-1.twimg.com",
  "abs-2.twimg.com",
  "abs-3.twimg.com",
  "abs-o-ft.twimg.com",
  "abs-o.twimg.com",
]);

const TARGET_HOST = "abs.twimg.com";

let rewriteCount = 0;

function report(tag, msg) {
  try {
    // 发给宿主 App 必须用 sendNativeMessage（sendMessage 只触发扩展内部
    // onMessage，到不了 App 的 MessageDelegate）。nativeApp 名 = 扩展 id。
    browser.runtime.sendNativeMessage("twimg-rewrite@anglesgirl.local", {
      tag: "twimg-rewrite",
      msg: `${tag} ${msg}`,
    });
  } catch (e) {
    // 没有接收端时静默
  }
}

report("loaded", `rewriting ${REWRITE_HOSTS.size} host(s) -> ${TARGET_HOST}`);

browser.webRequest.onBeforeRequest.addListener(
  (details) => {
    let url;
    try {
      url = new URL(details.url);
    } catch (e) {
      return {};
    }
    if (!REWRITE_HOSTS.has(url.hostname)) {
      return {};
    }
    // 只改 host，path/query/fragment 全部保留 —— 两边路径结构相同。
    url.hostname = TARGET_HOST;
    rewriteCount++;
    report("hit", `#${rewriteCount} ${details.type} ${details.url} -> ${url.toString()}`);
    return { redirectUrl: url.toString() };
  },
  { urls: ["*://*.twimg.com/*"] },
  ["blocking"]
);

report("listener-registered", "onBeforeRequest blocking listener active");
