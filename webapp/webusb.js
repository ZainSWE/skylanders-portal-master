// Skylanders Portal Master, web version: talks to Xbox 360 portals over WebUSB and runs the
// same Go engine as the Windows app (compiled to WebAssembly, window.PortalCore).
// The page's api() calls come here through window.PortalBackend instead of a server.
(() => {
  "use strict";
  const VENDOR = 0x1430; // Activision
  const NO_USB = "This browser can't reach USB devices. Open this page in Chrome or Edge on a computer, or Chrome on Android.";
  let core = null;
  const ready = new Promise((resolve) => {
    window.addEventListener("portalcore", () => { core = window.PortalCore; resolve(); }, { once: true });
  });
  const store = {
    get(k) { try { return localStorage.getItem(k) || ""; } catch (e) { return ""; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (e) {} },
  };

  // ---------- starting the engine ----------
  async function startCore() {
    const go = new Go();
    const bytes = await coreBytes();
    const { instance } = await WebAssembly.instantiate(bytes, go.importObject);
    go.run(instance);
    await ready;
    core.init(store.get("pm.settings"), store.get("pm.profiles"));
    if (navigator.usb) {
      navigator.usb.addEventListener("connect", (e) => openDevice(e.device));
      navigator.usb.addEventListener("disconnect", (e) => closeDevice(e.device));
      for (const d of await navigator.usb.getDevices()) if (d.vendorId === VENDOR) openDevice(d);
    }
    // A worker keeps time: timers on the page itself slow right down when the tab is in the background.
    const worker = new Worker(URL.createObjectURL(new Blob(["setInterval(() => postMessage(0), 20)"], { type: "text/javascript" })));
    worker.onmessage = tick;
  }
  async function coreBytes() {
    const b64 = window.PORTAL_CORE_GZ;
    const raw = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const stream = new Blob([raw]).stream().pipeThrough(new DecompressionStream("gzip"));
    return new Response(stream).arrayBuffer();
  }

  // ---------- portals ----------
  const portals = new Map(); // key -> {dev, outEp, inEp, queue, inflight, closed}
  let nextIndex = 1;
  function keyFor(dev) {
    if (dev.__pmKey) return dev.__pmKey;
    const pid = dev.productId.toString(16).padStart(4, "0");
    dev.__pmKey = dev.serialNumber ? `usb-1430-${pid}-${dev.serialNumber}` : `usb-1430-${pid}-${nextIndex++}`;
    return dev.__pmKey;
  }
  async function openDevice(dev) {
    if (dev.vendorId !== VENDOR) return;
    const key = keyFor(dev);
    if (portals.has(key)) return;
    const p = { dev, key, queue: [], inflight: 0, closed: false };
    portals.set(key, p);
    try {
      if (!dev.opened) await dev.open();
      if (dev.configuration === null) await dev.selectConfiguration(1);
      let chosen = null;
      for (const itf of dev.configuration.interfaces) {
        const alt = itf.alternates[0];
        const ins = alt.endpoints.filter(e => e.direction === "in"), outs = alt.endpoints.filter(e => e.direction === "out");
        if (ins.length && outs.length) { chosen = { itf, in: ins[0], out: outs[0] }; break; }
      }
      if (!chosen) throw new Error("This doesn't look like an Xbox 360 portal.");
      await dev.claimInterface(chosen.itf.interfaceNumber);
      p.inEp = chosen.in.endpointNumber; p.outEp = chosen.out.endpointNumber;
      core.addPortal(key, dev.productName || "Portal", chosen.out.packetSize, dev.productId.toString(16).padStart(4, "0"));
      readLoop(p);
    } catch (e) {
      portals.delete(key);
      problem = e.name === "SecurityError" || /access denied|Unable to claim/i.test(e.message)
        ? "The portal is in use by another program. Close the Skylanders Portal Master app on this PC (Turn off and quit), then reconnect."
        : "Couldn't open the portal: " + e.message;
      throw new Error(problem);
    }
  }
  function closeDevice(dev) {
    const key = dev.__pmKey;
    const p = key && portals.get(key);
    if (!p) return;
    p.closed = true;
    portals.delete(key);
    core.removePortal(key);
  }
  async function readLoop(p) {
    while (!p.closed) {
      try {
        const r = await p.dev.transferIn(p.inEp, 64);
        if (r.data && r.data.byteLength) core.message(p.key, new Uint8Array(r.data.buffer, r.data.byteOffset, r.data.byteLength));
      } catch (e) {
        if (!p.closed) closeDevice(p.dev);
        return;
      }
    }
  }
  function pump(p) {
    while (!p.closed && p.inflight < 6 && p.queue.length) {
      const data = p.queue.shift();
      p.inflight++;
      p.dev.transferOut(p.outEp, data).then(() => { p.inflight--; pump(p); }, () => { p.inflight--; if (!p.closed) closeDevice(p.dev); });
    }
  }

  // ---------- the clock ----------
  let problem = "";
  function tick() {
    if (!core) return;
    const r = core.tick();
    for (const { key, data } of r.packets) {
      const p = portals.get(key);
      if (!p) continue;
      if (p.queue.length > 400) p.queue.splice(0, p.queue.length - 400); // never build up a backlog
      p.queue.push(data);
      pump(p);
    }
    for (const s of r.sounds) playHere(s.style, s.kind);
    if (r.save) save();
  }
  function save() {
    const s = core.save();
    store.set("pm.settings", s.settings);
    store.set("pm.profiles", s.profiles);
  }

  // ---------- sounds on this device ----------
  let ctx = null;
  const buffers = new Map();
  function audio() {
    if (!ctx) { try { ctx = new AudioContext(); } catch (e) { return null; } }
    if (ctx.state === "suspended") ctx.resume().catch(() => {});
    return ctx;
  }
  ["pointerdown", "keydown"].forEach((ev) => window.addEventListener(ev, () => audio(), { once: true, capture: true }));
  function playHere(style, kind) {
    const c = audio();
    if (!c) return;
    const k = (kind === "place" ? "any" : style) + "/" + kind;
    let buf = buffers.get(k);
    if (!buf) {
      const m = core.sound(style, kind);
      buf = c.createBuffer(1, m.length, 22050);
      buf.copyToChannel(m, 0);
      buffers.set(k, buf);
    }
    const src = c.createBufferSource();
    src.buffer = buf; src.connect(c.destination); src.start();
  }

  // ---------- what the page calls instead of a server ----------
  const started = startCore();
  window.PortalBackend = {
    async request(path, opts = {}) {
      await started;
      const [p, query = ""] = path.split("?");
      if (p === "/api/connect") {
        if (!navigator.usb) throw new Error(NO_USB);
        let dev;
        try { dev = await navigator.usb.requestDevice({ filters: [{ vendorId: VENDOR }] }); }
        catch (e) { if (e.name === "NotFoundError") return null; throw e; } // closed the chooser
        problem = "";
        await openDevice(dev);
        return this.request("/api/state");
      }
      let body = opts.body;
      if (typeof body === "string") body = new TextEncoder().encode(body);
      else if (body instanceof ArrayBuffer) body = new Uint8Array(body);
      const r = core.request(p, query, body || null);
      if (r.error) throw new Error(r.error[0].toUpperCase() + r.error.slice(1) + ".");
      const st = JSON.parse(r.json);
      if (!navigator.usb) st.status.text = NO_USB;
      else if (problem && !st.portals) st.status.text = problem;
      return st;
    },
  };
})();
