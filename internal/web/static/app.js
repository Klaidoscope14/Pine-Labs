// Aegis web app: keyboard shortcuts, ⌘K palette, live refresh, confirmations.
(() => {
  const typing = (e) => /^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName) || e.target.isContentEditable;

  // Confirm destructive forms.
  document.querySelectorAll("form[data-confirm]").forEach((f) =>
    f.addEventListener("submit", (e) => { if (!confirm(f.dataset.confirm)) e.preventDefault(); }));

  // ⌘/Ctrl+Enter submits the compose form.
  document.querySelectorAll("textarea").forEach((t) =>
    t.addEventListener("keydown", (e) => { if ((e.metaKey || e.ctrlKey) && e.key === "Enter") t.form.requestSubmit(); }));

  // Command palette.
  const pal = document.getElementById("palette");
  const q = document.getElementById("pq"), list = document.getElementById("pl");
  const actions = [
    ["New run", "/new", "C"], ["Runs", "/runs", ""], ["Review", "/runs?view=review", ""],
    ["Done", "/runs?view=done", ""], ["Settings", "/settings", ""],
  ];
  document.querySelectorAll(".row").forEach((r) =>
    actions.push([r.querySelector(".rtitle").textContent, r.getAttribute("href"), r.querySelector(".rid").textContent]));
  let sel = 0, shown = [];
  const draw = () => {
    const s = q.value.toLowerCase();
    shown = actions.filter((a) => a[0].toLowerCase().includes(s) || a[2].toLowerCase().includes(s)).slice(0, 12);
    sel = Math.min(sel, Math.max(shown.length - 1, 0));
    list.replaceChildren(...shown.map((a, i) => {
      const li = document.createElement("li");
      li.className = i === sel ? "sel" : "";
      const t = document.createElement("span"); t.textContent = a[0];
      const k = document.createElement("span"); k.textContent = a[2];
      li.append(t, k);
      li.onclick = () => (location.href = a[1]);
      return li;
    }));
  };
  const open = () => { if (!pal) return; pal.hidden = false; q.value = ""; sel = 0; draw(); q.focus(); };
  const close = () => { if (pal) pal.hidden = true; };
  if (pal) {
    q.addEventListener("input", () => { sel = 0; draw(); });
    q.addEventListener("keydown", (e) => {
      if (e.key === "ArrowDown") { sel = Math.min(sel + 1, shown.length - 1); draw(); e.preventDefault(); }
      if (e.key === "ArrowUp") { sel = Math.max(sel - 1, 0); draw(); e.preventDefault(); }
      if (e.key === "Enter" && shown[sel]) location.href = shown[sel][1];
    });
    pal.addEventListener("click", (e) => { if (e.target === pal) close(); });
  }

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") { e.preventDefault(); pal && pal.hidden ? open() : close(); return; }
    if (e.key === "Escape") { close(); return; }
    if (typing(e) || e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === "c" && document.querySelector(".side")) location.href = "/new";
  });

  // Live refresh: reload when any run in the tenant changes, unless the user is typing.
  if (document.querySelector("[data-live]") && window.EventSource) {
    const es = new EventSource("/events");
    es.addEventListener("change", () => {
      const a = document.activeElement;
      if (a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) return;
      if (pal && !pal.hidden) return;
      location.reload();
    });
  }
})();

// Passkeys (WebAuthn): register in Settings; approve a run with a signed assertion.
(() => {
  const b64u = {
    dec: (s) => Uint8Array.from(atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4)), (c) => c.charCodeAt(0)),
    enc: (b) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""),
  };
  const post = (url, csrf, body) => fetch(url, {
    method: "POST", headers: { "X-CSRF-Token": csrf, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : "{}",
  }).then(async (r) => { const j = await r.json().catch(() => ({})); if (!r.ok) throw new Error(j.error || r.statusText); return j; });
  const say = (msg) => { const m = document.getElementById("pk-msg"); if (m) m.textContent = msg; else alert(msg); };

  const reg = document.querySelector("[data-passkey-register]");
  if (reg) reg.addEventListener("click", async () => {
    try {
      const o = (await post("/api/passkeys/register/begin", reg.dataset.csrf)).publicKey;
      o.challenge = b64u.dec(o.challenge);
      o.user.id = b64u.dec(o.user.id);
      (o.excludeCredentials || []).forEach((c) => (c.id = b64u.dec(c.id)));
      const c = await navigator.credentials.create({ publicKey: o });
      await post("/api/passkeys/register/finish", reg.dataset.csrf, {
        id: c.id, rawId: b64u.enc(c.rawId), type: c.type,
        response: { attestationObject: b64u.enc(c.response.attestationObject), clientDataJSON: b64u.enc(c.response.clientDataJSON),
          transports: c.response.getTransports ? c.response.getTransports() : [] },
      });
      location.reload();
    } catch (e) { say("Passkey not added: " + e.message); }
  });

  const appr = document.querySelector("[data-passkey-approve]");
  if (appr) appr.addEventListener("click", async () => {
    appr.disabled = true;
    try {
      const o = (await post(appr.dataset.passkeyApprove + "/approve/begin", appr.dataset.csrf)).publicKey;
      o.challenge = b64u.dec(o.challenge);
      (o.allowCredentials || []).forEach((c) => (c.id = b64u.dec(c.id)));
      const a = await navigator.credentials.get({ publicKey: o });
      await post(appr.dataset.passkeyApprove + "/approve/finish", appr.dataset.csrf, {
        id: a.id, rawId: b64u.enc(a.rawId), type: a.type,
        response: { authenticatorData: b64u.enc(a.response.authenticatorData), clientDataJSON: b64u.enc(a.response.clientDataJSON),
          signature: b64u.enc(a.response.signature), userHandle: a.response.userHandle ? b64u.enc(a.response.userHandle) : null },
      });
      setTimeout(() => location.reload(), 800);
    } catch (e) { appr.disabled = false; alert("Not approved: " + e.message); }
  });
})();
