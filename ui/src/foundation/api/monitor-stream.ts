import { authorizationHeaders, notifyUnauthorized } from "./session-token";

// A streaming decoder preserves partial UTF-8 and SSE frames across reads.
export async function readMonitorEvents(
  body: ReadableStream<Uint8Array>,
  onEvent: (data: string) => void,
) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      pending += decoder.decode(value, { stream: !done });
      let match: RegExpExecArray | null;
      while ((match = /\r?\n\r?\n/.exec(pending))) {
        const frame = pending.slice(0, match.index);
        pending = pending.slice(match.index + match[0].length);
        let kind = "message";
        const data: string[] = [];
        for (const line of frame.split(/\r?\n/)) {
          if (line.startsWith("event:")) kind = line.slice(6).trim();
          if (line.startsWith("data:"))
            data.push(line.slice(5).replace(/^ /, ""));
        }
        if (kind === "monitor" && data.length) onEvent(data.join("\n"));
      }
      if (done) return;
    }
  } finally {
    reader.releaseLock();
  }
}

export function subscribeMonitorStream(
  url: string,
  onEvent: (event: MessageEvent) => void,
) {
  const controller = new AbortController();
  let retryTimer: number | undefined;
  async function connect() {
    try {
      const response = await fetch(url, {
        credentials: "same-origin",
        headers: { Accept: "text/event-stream", ...authorizationHeaders() },
        signal: controller.signal,
        cache: "no-store",
      });
      if (response.status === 401) {
        notifyUnauthorized();
        return;
      }
      if (!response.ok || !response.body)
        throw new Error("Monitor stream unavailable");
      // Reconnect may have missed events; refresh the retained console queries.
      onEvent(new MessageEvent("monitor", { data: "{}" }));
      await readMonitorEvents(response.body, (data) =>
        onEvent(new MessageEvent("monitor", { data })),
      );
    } catch {
      /* Retry transient connection failures until this subscriber leaves. */
    }
    if (!controller.signal.aborted)
      retryTimer = window.setTimeout(() => {
        void connect();
      }, 3000);
  }
  void connect();
  return () => {
    controller.abort();
    window.clearTimeout(retryTimer);
  };
}
