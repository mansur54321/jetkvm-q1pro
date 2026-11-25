const { chromium } = require("playwright-core");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const [mode, origin, password, archive, ipFile] = process.argv.slice(2);
(async () => {
  const b = await chromium.launch({
    executablePath: "/usr/bin/chromium",
    headless: true,
    args: [
      "--no-sandbox",
      "--disable-dev-shm-usage",
      "--disable-background-networking",
    ],
  });
  const ctx = await b.newContext({ locale: "en-US", serviceWorkers: "block" });
  await ctx.addInitScript(() => {
    const o = RTCPeerConnection.prototype.createDataChannel;
    RTCPeerConnection.prototype.createDataChannel = function (...args) {
      const dc = o.apply(this, args);
      if (dc.label === "rpc") window.tsTest = { pc: this, dc };
      return dc;
    };
  });
  const p = await ctx.newPage();
  p.setDefaultTimeout(35000);
  function rpc(method, params = {}) {
    return p.evaluate(
      async ({ method, params }) => {
        const c = window.tsTest.dc;
        const id = Math.random().toString(16);
        return await new Promise((resolve, reject) => {
          const timeout = setTimeout(
            () => reject(new Error("timed out " + method)),
            25000,
          );
          function msg(e) {
            const m = JSON.parse(e.data);
            if (m.id !== id) return;
            clearTimeout(timeout);
            c.removeEventListener("message", msg);
            if (m.error)
              reject(
                new Error(
                  typeof m.error.data === "string"
                    ? m.error.data
                    : m.error.message,
                ),
              );
            else resolve(m.result);
          }
          c.addEventListener("message", msg);
          c.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
        });
      },
      { method, params },
    );
  }
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  try {
    if (mode === "media") {
      await p.goto(origin);
      const result = await p.evaluate(async () => {
        const pc = new RTCPeerConnection({ iceServers: [] });
        const video = document.createElement("video");
        video.muted = true;
        video.autoplay = true;
        video.playsInline = true;
        document.body.append(video);
        pc.ontrack = (event) => {
          video.srcObject = new MediaStream([event.track]);
          void video.play();
        };
        pc.addTransceiver("video", { direction: "recvonly" });
        try {
          await pc.setLocalDescription(await pc.createOffer());
          if (pc.iceGatheringState !== "complete")
            await new Promise((resolve, reject) => {
              const timer = setTimeout(
                () => reject(Error("fixture ICE gathering timeout")),
                5000,
              );
              pc.addEventListener("icegatheringstatechange", () => {
                if (pc.iceGatheringState === "complete") {
                  clearTimeout(timer);
                  resolve();
                }
              });
            });
          const response = await fetch("/offer", {
            method: "POST",
            body: JSON.stringify(pc.localDescription),
          });
          if (!response.ok)
            throw Error("fixture offer rejected: " + response.status);
          await pc.setRemoteDescription(await response.json());
          for (let i = 0; i < 90; i++) {
            const stats = await pc.getStats();
            const inbound = [...stats.values()].find(
              (s) => s.type === "inbound-rtp" && s.kind === "video",
            );
            const pair = [...stats.values()].find(
              (s) =>
                s.type === "candidate-pair" &&
                s.nominated &&
                s.state === "succeeded",
            );
            if ((inbound?.framesDecoded ?? 0) >= 3 && pair) {
              const remote = stats.get(pair.remoteCandidateId);
              return {
                frames: inbound.framesDecoded,
                packets: inbound.packetsReceived,
                width: video.videoWidth,
                height: video.videoHeight,
                remote: remote.address,
                protocol: remote.protocol,
              };
            }
            await new Promise((r) => setTimeout(r, 120));
          }
          throw Error("no decoded video through tailnet");
        } finally {
          pc.close();
        }
      });
      assert.ok(result.frames >= 3 && result.packets >= 3);
      assert.equal(result.width, 64);
      assert.equal(result.height, 48);
      assert.equal(result.remote, new URL(origin).hostname);
      assert.equal(result.protocol, "udp");
      console.log(
        "Synthetic RTP via Q1 userspace Tailscale PASS",
        JSON.stringify(result),
      );
      return;
    }
    await p.goto(origin);
    if (mode === "lan") {
      await p.locator('a[href="/welcome/mode"]').click();
      await p.locator('input[value="password"]').check();
      await p.locator('button[type="submit"]').click();
      await p.locator('input[name="password"]').fill(password);
      await p.locator('input[name="confirmPassword"]').fill(password);
      await p.locator('button[type="submit"]').click();
      await p.waitForFunction(() => window.tsTest?.dc.readyState === "open");
    } else {
      await p.locator('input[name="password"]').fill(password);
      await p.locator('button[type="submit"]').click();
      await p.waitForFunction(() => window.tsTest?.dc.readyState === "open");
    }
    if (mode === "lan") {
      await p.goto(origin + "/settings/network");
      await p.waitForFunction(() => window.tsTest?.dc.readyState === "open");
      const st = await rpc("getTailscaleStatus");
      assert.equal(st.installed, false);
      assert.equal(st.addon.enabled, false);
      console.log("before install", st.addon);
      const upload = p.waitForResponse(
        (r) =>
          r.url() === origin + "/device/tailscale-addon" &&
          r.request().method() === "POST",
        { timeout: 95000 },
      );
      await p.locator('input[type=file][accept=".tgz"]').setInputFiles(archive);
      const response = await upload;
      if (response.status() !== 204)
        throw Error(
          "upload " + response.status() + ": " + (await response.text()),
        );
      console.log("authenticated add-on upload", response.status());
      await rpc("setTailscaleControlURL", {
        controlURL: "http://192.0.2.1:3330",
      });
      await p.getByRole("button", { name: "Enable Tailscale" }).click();
      let url;
      for (let i = 0; i < 50; i++) {
        const s = await rpc("getTailscaleStatus");
        if (s.authURL) {
          url = s.authURL;
          break;
        }
        await wait(450);
      }
      assert.ok(
        url && url.startsWith("http://192.0.2.1:3330/auth/"),
        "missing private owner login URL",
      );
      const loginLink = p.getByRole("link", { name: url, exact: true });
      await loginLink.waitFor();
      const popupPromise = p.waitForEvent("popup");
      await loginLink.click();
      const popup = await popupPromise;
      await popup.getByText("approved test node").waitFor();
      await popup.close();
      let lastState;
      for (let i = 0; i < 160; i++) {
        const s = await rpc("getTailscaleStatus");
        if (s.backendState !== lastState) {
          console.log("Q1 registration state", s.backendState);
          lastState = s.backendState;
        }
        if (s.running) {
          const ip = s.self.tailscaleIPs.find((x) => !x.includes(":"));
          assert.ok(ip);
          fs.writeFileSync(ipFile, ip);
          console.log(
            "Q1 authorized in private tailnet",
            ip,
            s.addon.freeBytes,
          );
          break;
        }
        await wait(350);
      }
      assert.ok(fs.existsSync(ipFile));
    } else if (mode === "tail") {
      const result = await rpc("getVideoState");
      assert.equal(result.error, "device_unavailable");
      const stats = await p.evaluate(async () => {
        const { pc, dc } = window.tsTest;
        const stats = await pc.getStats();
        const pair = [...stats.values()].find(
          (s) =>
            s.type === "candidate-pair" &&
            s.nominated &&
            s.state === "succeeded",
        );
        const remote = pair && stats.get(pair.remoteCandidateId);
        return {
          session: pc.connectionState,
          rpc: dc.readyState,
          remote: remote?.address,
          protocol: remote?.protocol,
          iceServers: pc.getConfiguration().iceServers,
        };
      });
      assert.equal(stats.remote, new URL(origin).hostname);
      assert.equal(stats.protocol, "udp");
      assert.equal(stats.rpc, "open");
      assert.equal(stats.session, "connected");
      assert.equal(stats.iceServers.length, 0);
      console.log("Q1 PRIVATE TAILNET WEBRTC PASS", JSON.stringify(stats));
    } else if (mode === "disable") {
      const before = await rpc("getTailscaleStatus");
      assert.equal(before.running, true);
      await rpc("setQ1TailscaleEnabled", { enabled: false });
      const off = await rpc("getTailscaleStatus");
      assert.equal(off.addon.enabled, false);
      assert.equal(off.running, false);
      assert.equal(off.installed, true);
      await rpc("setQ1TailscaleEnabled", { enabled: true });
      for (let i = 0; i < 45; i++) {
        const s = await rpc("getTailscaleStatus");
        if (s.running) {
          assert.equal(s.self.tailscaleIPs[0], before.self.tailscaleIPs[0]);
          console.log("identity preserved across disable/enable");
          return;
        }
        await wait(350);
      }
      throw Error("identity did not resume");
    }
  } finally {
    await ctx.close();
    await b.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
