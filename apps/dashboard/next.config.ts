import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Nothing uses next/image (images are plain <img> elements), so the Image
  // Optimization API is off: /_next/image answers 404. next start reads this
  // file when it starts, so the Dockerfile copies it into the runtime image;
  // without it the server runs on the defaults and serves /_next/image. The
  // gateway closes that path as well (infra/nginx/life-os.conf.template).
  images: { unoptimized: true },
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
