import { expect, test, type APIRequestContext } from "@playwright/test";

type PublicServerURLResponse = {
  publicServerUrl: string;
};

type AdminAuthStatus = {
  enabled: boolean;
  authenticated: boolean;
  mustChange: boolean;
};

const testPublicServerURL =
  process.env.PLAYWRIGHT_IPTV_PUBLIC_URL ??
  "http://linearcast-playwright.test:8080";

async function authenticateAdmin(request: APIRequestContext) {
  const statusResponse = await request.get("/api/auth/status");
  await expect(statusResponse, "read the admin authentication status").toBeOK();
  const status = (await statusResponse.json()) as AdminAuthStatus;
  if (!status.enabled || status.authenticated) return;

  const password = process.env.PLAYWRIGHT_ADMIN_PASSWORD;
  if (!password) {
    throw new Error(
      "PLAYWRIGHT_ADMIN_PASSWORD is required when admin authentication is enabled",
    );
  }

  const loginResponse = await request.post("/api/auth/login", {
    data: { password },
  });
  await expect(loginResponse, "authenticate the Playwright API context").toBeOK();
  const loginStatus = (await loginResponse.json()) as AdminAuthStatus;
  if (!loginStatus.mustChange) return;

  const newPassword = process.env.PLAYWRIGHT_ADMIN_NEW_PASSWORD;
  if (!newPassword) {
    throw new Error(
      "PLAYWRIGHT_ADMIN_NEW_PASSWORD is required when the admin password must be changed",
    );
  }

  const changeResponse = await request.post("/api/auth/change-password", {
    data: {
      currentPassword: password,
      newPassword,
    },
  });
  await expect(changeResponse, "replace the disposable default password").toBeOK();
}

async function readPublicServerURL(request: APIRequestContext) {
  const response = await request.get("/api/public-server-url");
  await expect(response, "read the configured public server URL").toBeOK();
  return (await response.json()) as PublicServerURLResponse;
}

async function writePublicServerURL(
  request: APIRequestContext,
  publicServerUrl: string,
) {
  const response = await request.put("/api/public-server-url", {
    data: { publicServerUrl },
  });
  await expect(response, "save the configured public server URL").toBeOK();
  return (await response.json()) as PublicServerURLResponse;
}

test("configured public URL and IPTV guide endpoints are available", async ({
  request,
}) => {
  await authenticateAdmin(request);
  const original = await readPublicServerURL(request);

  try {
    await expect(
      writePublicServerURL(request, testPublicServerURL),
    ).resolves.toEqual({ publicServerUrl: testPublicServerURL });
    await expect(readPublicServerURL(request)).resolves.toEqual({
      publicServerUrl: testPublicServerURL,
    });

    const m3uResponse = await request.get("/api/m3u");
    await expect(m3uResponse, "serve the M3U playlist").toBeOK();
    expect(m3uResponse.headers()["content-type"]).toContain("mpegurl");
    expect(await m3uResponse.text()).toMatch(/^#EXTM3U(?:\r?\n|$)/);

    const xmltvResponse = await request.get("/api/xmltv");
    await expect(xmltvResponse, "serve the XMLTV guide").toBeOK();
    expect(xmltvResponse.headers()["content-type"]).toContain("xml");
    expect(await xmltvResponse.text()).toContain("<tv");
  } finally {
    await expect(
      writePublicServerURL(request, original.publicServerUrl),
    ).resolves.toEqual(original);
  }
});
