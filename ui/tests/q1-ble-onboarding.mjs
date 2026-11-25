// Exercise the standalone phone page with browser GATT objects and inspect only synthetic credentials.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { chromium } from "@playwright/test";

const html = await readFile(new URL("../public/q1pro-wifi.html", import.meta.url));
const server = createServer((_, res) => { res.writeHead(200, { "Content-Type": "text/html" }); res.end(html); });
await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
const browser = await chromium.launch({executablePath:process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE});
try {
  const page = await browser.newPage({ viewport: { width: 375, height: 740 }, locale: "ru-RU" });
  await page.addInitScript(() => {
    const chars = {};
    window.sentChunks = [];
    const notify = (key, bytes) => {
      const ch=chars[key];ch.value=new DataView(Uint8Array.from(bytes).buffer);
      ch.dispatchEvent(new Event("characteristicvaluechanged"));
    };
    window.q1Notify=notify;
    for (const n of [1,2,3,4]) {
      chars[n]=new EventTarget();
      chars[n].startNotifications=async () => {};
      chars[n].readValue=async () => new DataView(Uint8Array.of(1).buffer);
    }
    chars[3].writeValueWithResponse=async b=>window.sentChunks.push(Array.from(b));
    const device=new EventTarget();
    device.gatt={connect:async()=>({getPrimaryService:async()=>({getCharacteristic:async uuid=>chars[Number(uuid.at(-1))]})}),disconnect(){}};
    Object.defineProperty(navigator,"bluetooth",{value:{requestDevice:async opts=>{
      if(opts.filters[0].services[0]!=="00467768-6228-2272-4663-277478268000") throw new Error("unexpected service");
      return device;
    }}});
  });
  const address="http://127.0.0.1:"+server.address().port;
  const sent=[];
  page.on("request",r=>sent.push([r.method(),r.url()]));
  await page.goto(address);
  await page.getByRole("button",{name:"Выбрать Q1"}).click();
  await page.waitForFunction(()=>document.querySelector("#message").textContent.includes("Коротко"));
  assert.equal(await page.locator("#send").isEnabled(),false);
  await page.evaluate(()=>window.q1Notify(1,[2]));
  await page.getByLabel("Имя сети Wi-Fi").fill("Test сеть");
  await page.getByLabel("Пароль Wi-Fi").fill("synthetic-password-for-test");
  await page.getByRole("button",{name:"Подключить Q1"}).click();
  await page.waitForFunction(()=>window.sentChunks.length>=2 && document.querySelector("#pass").value==="");
  const chunks=await page.evaluate(()=>window.sentChunks);
  assert.ok(chunks.every(c=>c.length<=20));
  const frame=chunks.flat(), payload=[Buffer.byteLength("Test сеть"),...Buffer.from("Test сеть"),27,...Buffer.from("synthetic-password-for-test")];
  assert.deepEqual(frame.slice(0,-1),[1,payload.length,...payload]);
  assert.equal(frame.at(-1),frame.slice(0,-1).reduce((x,y)=>(x+y)&255,0));
  await page.evaluate(()=>{
    window.q1Notify(1,[3]);window.q1Notify(1,[4]); const dest=[...new TextEncoder().encode("http://192.0.2.10/")];
    const msg=[1,dest.length+1,dest.length,...dest];msg.push(msg.reduce((a,b)=>(a+b)&255,0));
    window.q1Notify(4,msg.slice(0,16));window.q1Notify(4,msg.slice(16));
  });
  assert.equal(await page.locator("#link").getAttribute("href"),"http://192.0.2.10/");
  assert.equal(await page.locator("#send").isEnabled(),false);
  assert.deepEqual(sent,[["GET",address+"/"]]);
  console.log("PASS: mobile Russian UI, button authorization, UTF-8 Wi-Fi payload, ATT chunks and checksum, result URL, no credential HTTP requests.");
} finally { await browser.close(); server.close(); }
