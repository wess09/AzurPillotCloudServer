// Cloudflare Worker：静态模板资产 + 少量路由重写。
// 不使用 workers.dev；由 wrangler.jsonc 绑定自定义域名。
const CORS = {
  'access-control-allow-origin': '*',
  'access-control-allow-methods': 'GET,HEAD,OPTIONS',
  'access-control-expose-headers': 'etag,x-manifest-signature,x-manifest-sha256',
};

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (request.method === 'OPTIONS') {
      return new Response(null, { status: 204, headers: CORS });
    }
    if (url.pathname === '/healthz') {
      return new Response('ok', { status: 200, headers: { ...CORS, 'content-type': 'text/plain' } });
    }
    if (url.pathname === '/api/v3/get/templates/manifest') {
      return serveManifest(request, env);
    }
    if (url.pathname === '/api/v3/get/templates/delta') {
      return serveDelta(request, env, url);
    }
    if (url.pathname === '/api/v3/get/mappings') {
      return serveJSONAsset(request, env, '/mappings.json', 'public, max-age=30');
    }
    return withCORS(await env.ASSETS.fetch(request));
  },
};

async function asset(request, env, path) {
  return env.ASSETS.fetch(new Request(new URL(path, request.url), request));
}

function withCORS(res) {
  const headers = new Headers(res.headers);
  for (const [k, v] of Object.entries(CORS)) headers.set(k, v);
  return new Response(res.body, { status: res.status, statusText: res.statusText, headers });
}

async function serveJSONAsset(request, env, path, cache) {
  const res = await asset(request, env, path);
  if (!res.ok) return withCORS(res);
  const headers = new Headers(res.headers);
  headers.set('content-type', 'application/json; charset=utf-8');
  headers.set('cache-control', cache);
  for (const [k, v] of Object.entries(CORS)) headers.set(k, v);
  return new Response(res.body, { status: 200, headers });
}

async function serveManifest(request, env) {
  const res = await asset(request, env, '/manifest.json');
  if (!res.ok) return withCORS(res);
  const sig = await (await asset(request, env, '/manifest.sig')).text();
  const sha = await (await asset(request, env, '/manifest.sha256')).text();
  const headers = new Headers(res.headers);
  headers.set('content-type', 'application/json; charset=utf-8');
  headers.set('cache-control', 'public, max-age=60');
  headers.set('x-manifest-signature', sig.trim());
  headers.set('x-manifest-sha256', sha.trim());
  for (const [k, v] of Object.entries(CORS)) headers.set(k, v);
  return new Response(res.body, { status: 200, headers });
}

async function serveDelta(request, env, url) {
  let from = url.searchParams.get('from') || '0';
  if (!/^\d{1,10}$/.test(from)) from = '0';
  let res = await asset(request, env, `/deltas/${from}.json`);
  if (res.status === 404) {
    res = await asset(request, env, '/deltas/0.json');
  }
  if (!res.ok) return withCORS(res);
  const headers = new Headers(res.headers);
  headers.set('content-type', 'application/json; charset=utf-8');
  headers.set('cache-control', 'public, max-age=30');
  for (const [k, v] of Object.entries(CORS)) headers.set(k, v);
  return new Response(res.body, { status: 200, headers });
}
