import {
  ApiError,
  UNAUTHORIZED_EVENT,
  apiFetch,
  apiFetchRaw,
  buildPath,
} from "../client";
import { afterEach, describe, expect, it, vi } from "vitest";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("API client", () => {
  it("encodes query values and omits nullish values", () => {
    expect(buildPath("/api/media", {
      q: "space / slash",
      offset: 0,
      enabled: false,
      omitted: undefined,
      empty: null,
    })).toBe("/api/media?q=space+%2F+slash&offset=0&enabled=false");
  });

  it("serializes JSON while preserving caller headers", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ ok: true }));
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/api/example", {
      method: "POST",
      query: { name: "A&B" },
      json: { enabled: true },
      headers: { "X-Linearcast-Test": "present" },
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(path).toBe("/api/example?name=A%26B");
    expect(init.body).toBe('{"enabled":true}');
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(headers.get("X-Linearcast-Test")).toBe("present");
  });

  it("maps the wire error field to ApiError.code", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      error: "already_member",
      message: "media is already attached",
      hint: "Refresh the schedule.",
    }, 409)));

    const error = await apiFetch("/api/example").catch((caught) => caught);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 409,
      code: "already_member",
      message: "media is already attached Refresh the schedule.",
    });
  });

  it("notifies the admin shell when a protected request expires", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      error: "unauthorized",
      message: "admin authentication required",
    }, 401)));
    const listener = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, listener);

    await expect(apiFetch("/api/now")).rejects.toBeInstanceOf(ApiError);

    expect(listener).toHaveBeenCalledOnce();
    window.removeEventListener(UNAUTHORIZED_EVENT, listener);
  });

  it("does not treat an auth-flow 401 as an expired loaded session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse({
      error: "unauthorized",
      message: "invalid admin password",
    }, 401)));
    const listener = vi.fn();
    window.addEventListener(UNAUTHORIZED_EVENT, listener);

    await expect(apiFetch("/api/auth/login")).rejects.toBeInstanceOf(ApiError);

    expect(listener).not.toHaveBeenCalled();
    window.removeEventListener(UNAUTHORIZED_EVENT, listener);
  });

  it("lets raw callers inspect non-success responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("warming", { status: 404 })));

    const response = await apiFetchRaw("/hls/channels/ch/stream.m3u8");

    expect(response.status).toBe(404);
    expect(await response.text()).toBe("warming");
  });

  it("preserves abort failures", async () => {
    const aborted = new DOMException("aborted", "AbortError");
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(aborted));

    await expect(apiFetch("/api/now")).rejects.toBe(aborted);
  });
});
