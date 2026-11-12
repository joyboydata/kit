/* <chat-thread>: server-HTML chat mount over SSE.
 *
 * The element owns HTTP; the server owns rendering. The composer POSTs
 * to `post`; the stream GETs `stream?hxchatns=<ns>` and each named
 * frame swaps the same-ID region inside this element only. Frames are
 * server-rendered HTML: the element never templates JSON.
 *
 * Isolation: ns is minted per instance and the server prefixes every
 * frame name with it, so two mounts on one page never swap each other.
 * Reconnect: a close while the composer is locked reopens after a
 * short backoff; Replay converges the missed frames.
 */
class ChatThread extends HTMLElement {
  constructor() {
    super();
    this._ns = ChatThread.mintNS();
    this._es = null;
    this._retryTimer = null;
    this._sending = false;
    this._onSubmit = (ev) => this._submit(ev);
  }

  static mintNS() {
    try {
      const b = new Uint8Array(4);
      crypto.getRandomValues(b);
      return Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
    } catch {
      return Math.random().toString(16).slice(2, 10);
    }
  }

  get prefix() {
    return this.getAttribute("prefix") || "chat";
  }

  get streamURL() {
    return this.getAttribute("stream") || "stream";
  }

  get postURL() {
    return this.getAttribute("post") || "messages";
  }

  connectedCallback() {
    // Scope IDs to this instance before opening the stream.
    for (const el of this.querySelectorAll("[data-chat-region]")) {
      el.id = `${this.prefix}-${this._ns}-${el.getAttribute("data-chat-region")}`;
    }
    this.addEventListener("submit", this._onSubmit);
    // Live from mount: Replay converges, then frames arrive.
    this._open();
  }

  disconnectedCallback() {
    this.removeEventListener("submit", this._onSubmit);
    this._close();
  }

  frameName(region) {
    return `${this.prefix}-${this._ns}-${region}`;
  }

  _submit(ev) {
    const form = ev.target;
    if (!(form instanceof HTMLFormElement)) return;
    ev.preventDefault();
    if (this._sending) return;
    this._sending = true;
    const body = new FormData(form);
    fetch(this.postURL, { method: "POST", body })
      .then((res) => {
        this._sending = false;
        if (!res.ok) return;
        const text = form.querySelector('[name="text"]');
        // The retry form reposts a fixed body; only clear free text.
        if (text instanceof HTMLTextAreaElement) text.value = "";
        // The mount stream is usually still open; redial only if not.
        if (!this._es || this._es.readyState !== 1) this._open();
      })
      .catch(() => {
        this._sending = false;
      });
  }

  _open() {
    this._close();
    const url = new URL(this.streamURL, document.baseURI);
    url.searchParams.set("hxchatns", this._ns);
    // The server prefixes frames with this mount's prefix, so each
    // mount swaps only its own regions.
    url.searchParams.set("prefix", this.prefix);
    const es = new EventSource(url.toString());
    this._es = es;
    for (const region of ["messages", "composer", "status", "error"]) {
      es.addEventListener(this.frameName(region), (ev) => this._swap(ev));
    }
    // Terminal redirect carries no URL; close is the unlock signal.
    es.addEventListener("redirect", () => this._close());
    es.onerror = () => {
      if (this._locked()) {
        // Drop while a run is open: reopen; Replay converges.
        this._close();
        this._retryTimer = setTimeout(() => this._open(), 300);
      } else {
        this._close();
      }
    };
  }

  _locked() {
    const composer = this.querySelector('[data-chat-region="composer"]');
    return !!(composer && composer.querySelector("[disabled]"));
  }

  _swap(ev) {
    const target = this.querySelector(`#${CSS.escape(ev.type)}`);
    if (!target || typeof ev.data !== "string") return;
    const tpl = document.createElement("template");
    tpl.innerHTML = ev.data.trim();
    const node = tpl.content.firstElementChild;
    if (node) target.replaceWith(node);
  }

  _close() {
    if (this._retryTimer) {
      clearTimeout(this._retryTimer);
      this._retryTimer = null;
    }
    if (this._es) {
      this._es.close();
      this._es = null;
    }
  }
}

customElements.define("chat-thread", ChatThread);
