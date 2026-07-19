import assert from 'node:assert/strict';

const origin = process.argv[2];
const expectedVersion = process.argv[3];
if (!origin || !expectedVersion) {
  throw new Error('usage: smoke-public-repo-wrangler.mjs <origin> <expected-version>');
}

async function request(path, init) {
  return fetch(`${origin}${path}`, { redirect: 'manual', ...init });
}

const deadline = Date.now() + 60_000;
while (true) {
  try {
    if ((await request('/health/ready')).ok) break;
  } catch {
    // The published container is still starting.
  }
  if (Date.now() >= deadline) throw new Error('published container did not become ready');
  await new Promise((resolveWait) => setTimeout(resolveWait, 500));
}

assert.deepEqual(await (await request('/health/live')).json(), { ok: true, version: expectedVersion });

const onboarding = await request('/onboarding');
assert.equal(onboarding.status, 200);
const html = await onboarding.text();
const scriptPath = /<script[^>]+src="([^"]+\.js)"/.exec(html)?.[1];
assert.ok(scriptPath, 'published onboarding page has no JavaScript bundle');
assert.equal((await request(scriptPath)).status, 200);

const initialAuth = await (await request('/auth/config')).json();
assert.equal(initialAuth.setupMode, true);
assert.deepEqual(initialAuth.providers, []);

const identity = await request('/api/v1/identity/configure', {
  method: 'POST',
  headers: { 'content-type': 'application/json' },
  body: JSON.stringify({ provider: 'github', allowedUsers: 'ranch-hand-public-smoke' }),
});
assert.equal(identity.status, 200);

const setupPage = await request('/setup/github-app');
assert.equal(setupPage.status, 200);
const setupHtml = await setupPage.text();
const encoded = /name="manifest" value="([^"]+)"/.exec(setupHtml)?.[1];
assert.ok(encoded, 'published GitHub App manifest form is missing');
const manifest = JSON.parse(encoded.replaceAll('&quot;', '"').replaceAll('&amp;', '&'));
assert.equal(manifest.redirect_url, `${origin}/setup/github-app/callback`);
assert.equal(manifest.callback_urls[0], `${origin}/auth/github/callback`);
assert.equal(manifest.hook_attributes, undefined, 'loopback manifest must not request a webhook');
assert.equal(manifest.default_events, undefined, 'loopback manifest must not request webhook events');

console.log('Published RepoWrangler Ranch Hand installation smoke passed.');
