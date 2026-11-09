import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Nothing uses next/image (images are plain <img> elements). This only
  // changes what <Image> would render: the production server still answers
  // /_next/image, so the gateway closes that path
  // (infra/nginx/life-os.conf.template).
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
