// twimg-rewrite —— 把 CF 上没有配置的 twimg 子域重写到 abs.twimg.com。
//
// 背景（2026-08-15 实测）：
//   x.com 前端硬编码引用 abs-0.twimg.com 加载 JS/CSS，但该域名在
//   Cloudflare 上**没有任何配置**：
//     - 明文 SNI 打任意 CF 边缘 → TLS 握手直接失败（curl code=000）
//     - SNI=abs.twimg.com + Host: abs-0.twimg.com → CF 返回 403
//     - 它实际走 Fastly：x-tw-cdn: FT，CNAME abs-zero.twimg.com
//       → 104.244.43.131（Twitter 自有段）
//   而 abs.twimg.com 虽然 DNS 也指向 Fastly，CF 边缘却接受它的 ECH
//   握手（cloudflare-ech.com 证书）→ 可以强改强注、走 ECH 隐藏 SNI。
//
//   于是 echdoh 对 abs-0 只能 fail-closed（返回空 A 记录，绝不回退明文
//   直连，否则 SNI 泄漏），代价是 /account/access 等页面元素缺失、
//   解封按钮点不了。
//
// 解法：请求发出前把 host 换成 abs.twimg.com。
//   内容等价性已实测：同一路径 /responsive-web/client-web/vendor.*.js
//   两边都是 200、624557 字节，逐字节比对仅差 40 字节 —— 是 Sentry
//   release id（构建批次不同），功能代码完全一致。
//   x.com 首页 integrity=（SRI）计数为 0，换域名不会触发完整性校验失败。
//
// 为什么用 webRequest 而不是 307 重定向：
//   307 得由服务器发，我们在 DNS 层发不出来；而且浏览器按新 URL 重发会
//   多一次往返。webRequest 在请求发出前改写，零额外往返。
//
// 为什么用 MV2 webRequestBlocking 而不是 MV3 declarativeNetRequest：
//   GeckoView 的 dNR 支持程度未经验证，而 webRequest 阻塞式重定向在
//   Gecko 上是长期稳定 API。GeckoView 内置扩展（installBuiltIn）不受
//   Firefox 桌面版 MV2 弃用影响。

// CF 上没有配置、必须重写的 twimg 子域。
// 2026-08-15 DNS 实测：abs-1/2/3、abs-vod 全部 NXDOMAIN（不存在，
// 豆包给的名单大部分是编的）；只有 abs-0 真实存在且被 x.com 引用。
// abs-o-ft（CNAME abs-o.twimg.com → 104.244.42.x）存在但本机完全连不通，
// 也未在实际日志中出现 —— 一并映射，出现即兜住。
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
    console.log(
      `[twimg-rewrite] #${rewriteCount} ${details.type} ${details.url} -> ${url.toString()}`
    );
    return { redirectUrl: url.toString() };
  },
  { urls: ["*://*.twimg.com/*"] },
  ["blocking"]
);

console.log(
  `[twimg-rewrite] loaded, rewriting ${REWRITE_HOSTS.size} host(s) -> ${TARGET_HOST}`
);
