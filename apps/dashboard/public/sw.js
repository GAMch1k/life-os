// The Life OS service worker. It exists so the Dashboard installs as an app
// (home screen, Share → Life OS); it does nothing else. It has no fetch
// handler and no cache: every page, API response, and shared item goes to the
// network exactly as without it, so nothing private is ever stored on the
// phone. See docs/frontend/dashboard.md ("Install on your phone").

self.addEventListener("install", () => {
  // A new version takes over at once; there is nothing to migrate.
  self.skipWaiting();
});

self.addEventListener("activate", (event) => {
  event.waitUntil(self.clients.claim());
});
