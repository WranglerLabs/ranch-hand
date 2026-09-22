import assert from "node:assert/strict";
import test from "node:test";
import { verifyPublicRegistryImage } from "./verify-public-registry-image.mjs";

const digest = `sha256:${"a".repeat(64)}`;
const image = `ghcr.io/wranglerlabs/repo-wrangler-server@${digest}`;

test("uses an anonymous token to verify the exact manifest", async () => {
  const requests = [];
  const fakeFetch = async (url, options = {}) => {
    requests.push({ url: String(url), options });
    return requests.length === 1
      ? new Response(JSON.stringify({ token: "anonymous" }), { status: 200 })
      : new Response("{}", { status: 200 });
  };
  await verifyPublicRegistryImage(image, fakeFetch);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].options.headers.Authorization, undefined);
  assert.equal(requests[1].options.headers.Authorization, "Bearer anonymous");
  assert.match(requests[1].url, new RegExp(`/manifests/${digest}$`));
});

test("rejects a private registry image", async () => {
  await assert.rejects(
    () => verifyPublicRegistryImage(image, async () => new Response("denied", { status: 401 })),
    /anonymous registry token request failed with HTTP 401/,
  );
});

test("rejects floating image references", async () => {
  await assert.rejects(
    () => verifyPublicRegistryImage("ghcr.io/wranglerlabs/repo-wrangler-server:latest"),
    /digest-pinned ghcr.io reference/,
  );
});
