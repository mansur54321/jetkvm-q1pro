const assert = require("node:assert/strict");
const { randomBytes } = require("node:crypto");
const { chromium } = require("playwright-core");

async function checkBrowser(browser, origin) {
  const external = new Set();
  const localSockets = new Set();
  const context = await browser.newContext({ locale: "en-US", serviceWorkers: "block" });
  const allowedSocket = origin.replace("http:", "ws:");

  function allow(address, allowed) {
    const url = new URL(address);
    if (url.origin === allowed) return true;
    external.add(url.origin + url.pathname);
    return false;
  }
  context.on("request", request => {
    if (/^https?:/.test(request.url())) allow(request.url(), origin);
  });
  await context.route(/^https?:/, route =>
    allow(route.request().url(), origin) ? route.continue() : route.abort("blockedbyclient"));
  await context.routeWebSocket(/.*/, socket => {
    if (allow(socket.url(), allowedSocket)) {
      localSockets.add(new URL(socket.url()).pathname);
      socket.connectToServer();
    } else {
      socket.close({ code: 1008, reason: "external address blocked" });
    }
  });

  // Observe the application's real peer and channel without changing ICE or SDP.
  await context.addInitScript(() => {
    const create = RTCPeerConnection.prototype.createDataChannel;
    RTCPeerConnection.prototype.createDataChannel = function (...args) {
      const channel = create.apply(this, args);
      if (channel.label === "rpc") window.q1ObservedRpc = { peer: this, channel };
      return channel;
    };
  });

  const page = await context.newPage();
  page.setDefaultTimeout(20000);
  const status = () => page.evaluate(async () => {
    const r = await fetch("/device/status");
    return [r.status, await r.json()];
  });
  try {
    await page.goto(origin);
    await page.locator('a[href="/welcome/mode"]').click();
    assert.deepEqual(await status(), [200, { isSetup: false, sku: "oraybox-q1pro" }]);
    await page.locator('input[value="password"]').check();
    await page.locator('button[type="submit"]').click();
    const password = randomBytes(18).toString("hex");
    await page.locator('input[name="password"]').fill(password);
    await page.locator('input[name="confirmPassword"]').fill(password);
    const setup = page.waitForResponse(r => r.url() === origin + "/device/setup" && r.request().method() === "POST");
    await page.locator('button[type="submit"]').click();
    assert.equal((await setup).status(), 200);
    assert.deepEqual(await status(), [200, { isSetup: true, sku: "oraybox-q1pro" }]);
    await context.clearCookies();
    await page.goto(origin);
    await page.locator('input[name="password"]').fill(password);
    const login = page.waitForResponse(r => r.url() === origin + "/auth/login-local" && r.request().method() === "POST");
    await page.locator('button[type="submit"]').click();
    assert.equal((await login).status(), 200);
    await page.waitForFunction(() => window.q1ObservedRpc?.channel.readyState === "open");
    const result = await page.evaluate(async () => {
      const { peer, channel } = window.q1ObservedRpc;
      const id = "q1-offline-getVideoState";
      const reply = await new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(Error("getVideoState timed out")), 10000);
        function onMessage(event) {
          const message = JSON.parse(event.data);
          if (message.id !== id) return;
          clearTimeout(timer);
          channel.removeEventListener("message", onMessage);
          if (message.error) reject(Error(message.error.message));
          else resolve(message.result);
        }
        channel.addEventListener("message", onMessage);
        channel.send(JSON.stringify({ jsonrpc: "2.0", id, method: "getVideoState", params: {} }));
      });
      const stats = await peer.getStats();
      const pair = [...stats.values()].find(s => s.type === "candidate-pair" && s.nominated && s.state === "succeeded");
      const remote = pair && stats.get(pair.remoteCandidateId);
      return { origin: location.origin, secureContext: window.isSecureContext,
        peer: peer.connectionState, channel: channel.readyState, remote: remote?.address,
        iceServers: peer.getConfiguration().iceServers.flatMap(s => s.urls),
        video: { ready: reply.ready, error: reply.error, width: reply.width, height: reply.height } };
    });
    assert.deepEqual(result, { origin: "http://192.0.2.50", secureContext: false,
      peer: "connected", channel: "open", remote: "192.0.2.50",
      iceServers: [], video: { ready: false, error: "device_unavailable", width: 0, height: 0 } });
    assert.ok(localSockets.has("/webrtc/signaling/client"));
    await page.getByRole("button", { name: "Settings", exact: true }).click();
    await page.waitForURL(origin + "/settings/general");
    await page.getByText("Configure device settings and update preferences", { exact: true }).waitFor();
    return { browser: browser.version(), setup: 200, login: 200, page: "/settings/general", ...result };
  } finally {
    await context.close();
    assert.deepEqual([...external], [], "Unexpected external HTTP or WebSocket URLs: " + [...external].join(", "));
  }
}

(async () => {
  const origin = process.argv[2];
  // Chromium follows a redirect after Playwright routes the initial request.
  // Send all other destinations to an unused port inside the private namespace.
  const bypass = "<-loopback>;" + origin + ";" + origin.replace("http:", "ws:");
  const browser = await chromium.launch({ executablePath: process.env.CHROMIUM || "/usr/bin/chromium", headless: true,
    args: ["--no-sandbox", "--proxy-server=http://127.0.0.1:1", "--proxy-bypass-list=" + bypass,
      "--disable-background-networking", "--disable-dev-shm-usage"] });
  try {
    const result = await checkBrowser(browser, origin);
    console.log("PASS", JSON.stringify({ ...result, externalRequests: [] }));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
