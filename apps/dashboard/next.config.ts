import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Nothing uses next/image (images are plain <img> elements), so the Image
  // Optimization API is off: /_next/image answers 404. next start reads this
  // file when it starts, so the Dockerfile copies it into the runtime image;
  // without it the server runs on the defaults and serves /_next/image. The
  // gateway closes that path as well (infra/nginx/life-os.conf.template).
  images: { unoptimized: true },
  // Better Auth and the PostgreSQL driver load from node_modules at run
  // time instead of being bundled: one copy in the process, shared by the
  // proxy (src/proxy.ts) and the routes, and Node's own builds (node:crypto
  // scrypt for passwords).
  serverExternalPackages: ["better-auth", "@better-auth/passkey", "pg"],
  async headers() {
    return [
      {
        // The service worker (public/sw.js) is revalidated on every check,
        // so a new version reaches installed apps at their next start.
        source: "/sw.js",
        headers: [{ key: "Cache-Control", value: "no-cache" }],
      },
    ];
  },
};

export default nextConfig;
