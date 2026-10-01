// Optional: NODE_PATH=<your runtime node_modules> node scripts/browser-e2e.cjs
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const base = process.env.GATEWAY_URL || "http://localhost:18080";
(async () => {
  const browser = await chromium.launch({
    headless: true,
    channel: process.env.BROWSER_CHANNEL || "chrome",
  });
  const a = await browser.newContext(),
    b = await browser.newContext();
  const pa = await a.newPage(),
    pb = await b.newPage();
  const errors = [];
  for (const p of [pa, pb]) p.on("pageerror", (e) => errors.push(e.message));
  const view = (p, v) =>
    p.waitForFunction(
      (v) => document.querySelector("#view").textContent === v,
      v,
    );
  const seq = (p, n) =>
    p.waitForFunction(
      (n) => Number(document.querySelector("#sequence").textContent) === n,
      n,
    );
  const clickSquare = (p, s) =>
    p.locator(`button[aria-label="${s}"],button[aria-label^="${s} "]`).click();
  try {
    await Promise.all([pa.goto(base), pb.goto(base)]);
    await Promise.all([view(pa, "Home"), view(pb, "Home")]);
    assert.notEqual(
      await pa.locator("#client-id").textContent(),
      await pb.locator("#client-id").textContent(),
    );
    await pa.locator("#nickname").fill("Browser moon");
    await pa.locator("#rename").click();
    await pa.locator("#find").click();
    await view(pa, "Searching");
    await pa.locator("#cancel").click();
    await view(pa, "Home");
    await pa.locator("#find").click();
    await pb.locator("#find").click();
    await Promise.all([view(pa, "Game"), view(pb, "Game")]);
    await Promise.all([
      pa.waitForFunction(() =>
        document.querySelector("#owner").textContent.startsWith("worker-"),
      ),
      pb.waitForFunction(() =>
        document.querySelector("#owner").textContent.startsWith("worker-"),
      ),
    ]);
    const gameId = await pa.locator("#game-id").textContent();
    assert.equal(await pb.locator("#game-id").textContent(), gameId);
    const white = (await pa.locator("#message").textContent()).includes(
      "You are white",
    )
      ? pa
      : pb;
    const black = white === pa ? pb : pa;
    const whiteContext = white === pa ? a : b;
    await clickSquare(white, "f2");
    await clickSquare(white, "f3");
    await Promise.all([seq(pa, 1), seq(pb, 1)]);
    await whiteContext.setOffline(true);
    await clickSquare(black, "e7");
    await clickSquare(black, "e5");
    await seq(black, 2);
    await whiteContext.setOffline(false);
    await seq(white, 2);
    const oldEpoch = Number(await white.locator("#epoch").textContent());
    await white.locator("#migrate").click();
    await white.waitForFunction(
      (n) => Number(document.querySelector("#epoch").textContent) > n,
      oldEpoch,
    );
    await clickSquare(white, "g2");
    await clickSquare(white, "g4");
    await Promise.all([seq(pa, 3), seq(pb, 3)]);
    await clickSquare(black, "d8");
    await clickSquare(black, "h4");
    await Promise.all([view(pa, "Finished"), view(pb, "Finished")]);
    await white.locator("#replay").click();
    assert.equal(await white.locator("#replay-position").isVisible(), true);
    await white.locator("#replay-position").fill("4");
    await white.locator("#replay-position").dispatchEvent("input");
    await white.screenshot({
      path: process.env.SCREENSHOT_PATH || "/tmp/moonchess-browser-e2e.png",
      fullPage: true,
    });
    await pa.reload();
    await view(pa, "Finished");
    assert.equal(await pa.locator("#game-id").textContent(), gameId);
    await pa.locator("#again").click();
    await view(pa, "Searching");
    await pb.locator("#again").click();
    await Promise.all([view(pa, "Game"), view(pb, "Game")]);
    assert.notEqual(await pa.locator("#game-id").textContent(), gameId);
    assert.equal(
      await pa.locator("#game-id").textContent(),
      await pb.locator("#game-id").textContent(),
    );
    assert.deepEqual(errors, []);
    console.log(
      "PASS: two isolated browsers, Home/Searching/Game/Finished, alternating moves, SSE reconnect, migrate, replay, refresh, play-again; no page errors",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exitCode = 1;
});
