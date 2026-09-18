import { afterEach, describe, expect, test, vi } from "vitest";
import { readMonitorEvents, subscribeMonitorStream } from "./monitor-stream";
import { setSessionToken } from "./session-token";

function stream(text: string) {
  const bytes = new TextEncoder().encode(text);
  return new ReadableStream<Uint8Array>({
    start(controller) {
      for (const byte of bytes) controller.enqueue(new Uint8Array([byte]));
      controller.close();
    },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  setSessionToken("");
});

describe("authenticated monitor stream", () => {
  test("decodes split UTF-8, CRLF frames, comments, and multiline data", async () => {
    const events: string[] = [];
    await readMonitorEvents(
      stream(
        ': heartbeat\r\n\r\nevent: monitor\r\ndata: {"path":"café"}\r\n\r\nevent: monitor\ndata: first\ndata: second\n\nevent: ignored\ndata: no\n\n',
      ),
      (data) => events.push(data),
    );
    expect(events).toEqual(['{"path":"café"}', "first\nsecond"]);
  });

  test("sends bearer headers, refreshes on reconnect, and stops retries on cleanup", async () => {
    vi.useFakeTimers();
    setSessionToken("test-stream-token");
    const fetch = vi
      .fn()
      .mockImplementation(
        async () =>
          new Response(stream('event: monitor\ndata: {"type":"agents"}\n\n')),
      );
    vi.stubGlobal("fetch", fetch);
    const receive = vi.fn();
    const stop = subscribeMonitorStream("/v1/monitor/stream", receive);
    await vi.advanceTimersByTimeAsync(0);
    expect(fetch.mock.calls[0][1].headers.Authorization).toBe(
      "Bearer test-stream-token",
    );
    expect(receive.mock.calls.map(([event]) => event.data)).toEqual([
      "{}",
      '{"type":"agents"}',
    ]);
    await vi.advanceTimersByTimeAsync(3000);
    expect(fetch).toHaveBeenCalledTimes(2);
    stop();
    await vi.advanceTimersByTimeAsync(6000);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(fetch.mock.calls[0][1].signal.aborted).toBe(true);
  });

  test("signals expired authorization and does not reconnect with a rejected token", async () => {
    vi.useFakeTimers();
    const expired = vi.fn();
    window.addEventListener("afs:unauthorized", expired);
    const fetch = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 401 }));
    vi.stubGlobal("fetch", fetch);
    const stop = subscribeMonitorStream("/v1/monitor/stream", vi.fn());
    await vi.advanceTimersByTimeAsync(6000);
    expect(expired).toHaveBeenCalledOnce();
    expect(fetch).toHaveBeenCalledOnce();
    stop();
    window.removeEventListener("afs:unauthorized", expired);
  });
});
