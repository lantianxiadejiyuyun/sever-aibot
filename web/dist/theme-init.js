/* Runs synchronously before first paint. Kept external so the Go control
 * plane's strict CSP (script-src 'self') permits it. */
(function () {
  var mode = 'auto'
  try {
    mode = localStorage.getItem('aibot-theme-mode') || 'auto'
  } catch (e) {
    /* storage may be unavailable in private mode */
  }
  if (mode !== 'light' && mode !== 'dark') mode = 'auto'

  var mql = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null
  var dark = mode === 'dark' || (mode === 'auto' && mql && mql.matches)
  var root = document.documentElement

  root.dataset.theme = dark ? 'dark' : 'light'
  root.dataset.themeMode = mode
  root.style.colorScheme = dark ? 'dark' : 'light'

  var themeMeta = document.getElementById('theme-color')
  if (themeMeta) themeMeta.setAttribute('content', dark ? '#0f141a' : '#f4f7fb')

  var statusMeta = document.getElementById('apple-status')
  if (statusMeta) statusMeta.setAttribute('content', dark ? 'black-translucent' : 'default')
})()
