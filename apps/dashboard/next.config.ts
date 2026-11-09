import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Nothing uses next/image (images are plain <img> elements), so the Image
  // Optimization API is off: /_next/image answers 404 without fetching or
  // decoding anything, which keeps that endpoint's advisories out of reach.
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
