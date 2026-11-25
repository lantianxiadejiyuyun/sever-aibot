import { fileURLToPath, URL } from 'node:url'
import { defineConfig, type Plugin } from 'vite'
import { createHash } from 'node:crypto'
import vue from '@vitejs/plugin-vue'

const target = process.env.AIBOT_PROXY_TARGET || 'http://127.0.0.1:7400'

// Only build-emitted static files are eligible for Cache Storage. Never learn
// cache entries from API responses, route navigations or terminal requests.
function staticShellWorker(): Plugin {
  return {
    name: 'static-shell-worker',
    apply: 'build',
    enforce: 'post',
    generateBundle(_options, bundle) {
      const files = Object.keys(bundle).filter(name => /^assets\/.+\.(js|css|woff2?|svg|png)$/.test(name)).map(name => `/${name}`).sort()
      const version = createHash('sha256').update(files.join('\n')).digest('hex').slice(0, 16)
      const allowlist = [...files, '/favicon.svg', '/theme-init.js', '/manifest.webmanifest', '/offline.html']
      this.emitFile({ type: 'asset', fileName: 'sw.js', source: `
const CACHE = 'aibot-static-${version}';
const STATIC = ${JSON.stringify(allowlist)};
self.addEventListener('install', event => {
  event.waitUntil(caches.open(CACHE).then(cache => cache.addAll(STATIC.map(url => new Request(url, {credentials:'omit', cache:'reload'})))));
});
self.addEventListener('activate', event => {
  event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(key => key.startsWith('aibot-static-') && key !== CACHE).map(key => caches.delete(key)))));
});
self.addEventListener('fetch', event => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/agent/') || url.pathname.startsWith('/terminal/') || url.pathname === '/healthz') return;
  if (req.mode === 'navigate') {
    event.respondWith(fetch(req).catch(() => caches.open(CACHE).then(cache => cache.match('/offline.html'))));
    return;
  }
  if (!url.search && STATIC.includes(url.pathname)) {
    event.respondWith(caches.open(CACHE).then(cache => cache.match(url.pathname)).then(cached => cached || fetch(req)));
  }
});
` })
    },
  }
}

export default defineConfig({
  plugins: [vue(), staticShellWorker()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    proxy: {
      '/api': {
        target,
        changeOrigin: false,
      },
      '/agent': {
        target,
        changeOrigin: false,
        ws: true,
      },
      '/healthz': {
        target,
        changeOrigin: false,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  define: {
    __APP_VERSION__: JSON.stringify(process.env.npm_package_version || '0.1.0'),
    __BUILD_TIME__: JSON.stringify(new Date().toISOString()),
  },
})
