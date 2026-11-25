
const CACHE = 'aibot-static-434b7cd748641d2d';
const STATIC = ["/assets/AIView-BAg24vOB.css","/assets/AIView-BVXuM_2U.js","/assets/AppShell-Bxuk3HHc.js","/assets/AssetsView-BWwKQ26j.js","/assets/AuditView-C7uRIx0t.js","/assets/ChangesView-B7SUi8DS.css","/assets/ChangesView-Bm1NLWoa.js","/assets/CredentialsView-DlixwzuA.js","/assets/DataSourcesView-B77lwyC5.js","/assets/DataSourcesView-BIav35JN.css","/assets/DeviceCreateView-DiYqbsS4.js","/assets/DeviceDetailView-f6lSFGl-.js","/assets/DeviceEditView-CIETh1cx.js","/assets/DeviceForm.vue_vue_type_script_setup_true_lang-CsYGICoH.js","/assets/DevicesView-Dz2P-bVh.js","/assets/EmptyState.vue_vue_type_script_setup_true_lang-CnfDRR7q.js","/assets/FormField.vue_vue_type_script_setup_true_lang-Dleqh1DB.js","/assets/ForwardCreateView-B-lfWoxf.js","/assets/ForwardDetailView-CDgC-Qqp.js","/assets/ForwardEditView-CJFMTzyr.js","/assets/ForwardForm.vue_vue_type_script_setup_true_lang-C2jhc-ao.js","/assets/ForwardsView-Bcjf2kc_.js","/assets/InspectionsView-gMuajFGi.js","/assets/InspectionsView-z9O8ZLQe.css","/assets/LoginView-DOZlVhRt.js","/assets/MetricHistory-BIZoprla.css","/assets/MetricHistory-DZJpkxQf.js","/assets/MetricsView-DNDN7ZVa.js","/assets/MetricsView-DwSRp5MG.css","/assets/NodeCreateView-H29IFOKD.js","/assets/NodeDetailView-Czm4hC9i.js","/assets/NodeEditView-gxNF9fyZ.js","/assets/NodeForm.vue_vue_type_script_setup_true_lang-DSh48tvj.js","/assets/NodesView-DZZ_yyer.js","/assets/NotFoundView-DJXsHcqu.js","/assets/NotificationsView-DVFj5Ki1.css","/assets/NotificationsView-VVYRJFw0.js","/assets/OverviewView-UMPhXtGF.js","/assets/PageHeader.vue_vue_type_script_setup_true_lang-lNGMoRl9.js","/assets/SecretDialog.vue_vue_type_script_setup_true_lang-CKwLC9g1.js","/assets/SettingsView-1WD9t-pg.js","/assets/StepUpDialog.vue_vue_type_script_setup_true_lang-CGfk9AD9.js","/assets/TerminalView-B5wrqBot.css","/assets/TerminalView-DS1GG4ka.js","/assets/ThemeToggle.vue_vue_type_script_setup_true_lang-DpaX5cYi.js","/assets/_plugin-vue_export-helper-DlAUqK2U.js","/assets/arrow-left-right-BqlI09LW.js","/assets/chevron-left-IpR-ZV3X.js","/assets/clipboard-gcNkRBTC.js","/assets/format-D3fj172a.js","/assets/forwardRuntime-ddm1Bk38.js","/assets/index-BAVYMNvp.js","/assets/index-D2YkBc2e.css","/assets/key-round-Db4PhEgz.js","/assets/network-Bd5z95Lp.js","/assets/node-Cw0fOelk.js","/assets/plus-Dl1580X2.js","/assets/refresh-cw-BeqXP38X.js","/assets/save-CzrMieqO.js","/assets/scroll-text-BHwh5E95.js","/assets/search-CvdVPI5Q.js","/assets/server-C_SrD0-q.js","/assets/sun-BJUt34z-.js","/assets/trash-2-BJSF8mAO.js","/assets/useLogout-DAncdUvG.js","/favicon.svg","/theme-init.js","/manifest.webmanifest","/offline.html"];
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
