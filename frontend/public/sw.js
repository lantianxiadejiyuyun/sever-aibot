// Only the offline notice is cached; authenticated responses never enter the cache.
self.addEventListener('push', (event) => {
  let payload
  try {
    payload = event.data?.json()
  } catch {
    // Still show an update when the browser delivers an unreadable push.
  }
  event.waitUntil(self.registration.showNotification(
    typeof payload?.title === 'string' ? payload.title : '端序 · 巡检通知',
    {
      body: typeof payload?.body === 'string' ? payload.body : '打开工作台查看巡检记录。',
      tag: typeof payload?.tag === 'string' ? payload.tag : undefined,
      icon: '/icon-192.png',
    },
  ))
})
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  event.waitUntil(
    clients
      .matchAll({ type: 'window', includeUncontrolled: true })
      .then((pages) =>
        pages.length ? pages[0].focus() : clients.openWindow('/'),
      ),
  )
})
const cacheName = 'relay-offline-v1'
self.addEventListener('install', (event) =>
  event.waitUntil(caches.open(cacheName).then((c) => c.add('/offline.html'))),
)
self.addEventListener('fetch', (event) => {
  if (
    event.request.mode === 'navigate' &&
    new URL(event.request.url).origin === self.location.origin &&
    !new URL(event.request.url).pathname.startsWith('/api/')
  )
    event.respondWith(
      fetch(event.request).catch(() => caches.match('/offline.html')),
    )
})
