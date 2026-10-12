#!/usr/bin/env node
// Draws the Life OS app icons (the home screen icon of the installed
// Dashboard) and writes them as PNGs to public/icons/. The mark is a capture
// button, a ring around a dot, light on the theme's primary colour (#171717).
// It needs no packages: shapes are sampled 4×4 per pixel and the PNG is
// written with node:zlib. Run it from apps/dashboard after changing the
// design and commit the PNGs:
//
//   node scripts/generate-icons.mjs

import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";

const BACKGROUND = [0x17, 0x17, 0x17]; // --primary, oklch(0.205 0 0)
const MARK = [0xfa, 0xfa, 0xfa]; // --primary-foreground, oklch(0.985 0 0)
const SAMPLES = 4;

// Sizes are fractions of the icon. An "any" icon is a rounded square with
// transparent corners; a maskable one fills the square, and its mark stays
// inside the central circle of radius 0.4 that every mask shape keeps. The
// badge is the mark alone on transparent: Android shows a notification's
// badge in the status bar as a silhouette of its opaque pixels.
const DESIGNS = {
  any: { corner: 0.225, outer: 0.3, inner: 0.215, dot: 0.125 },
  maskable: { corner: 0, outer: 0.25, inner: 0.18, dot: 0.105 },
  badge: { markOnly: true, outer: 0.46, inner: 0.33, dot: 0.19 },
};

const ICONS = [
  { file: "icon-192.png", size: 192, design: "any" },
  { file: "icon-512.png", size: 512, design: "any" },
  { file: "icon-maskable-192.png", size: 192, design: "maskable" },
  { file: "icon-maskable-512.png", size: 512, design: "maskable" },
  { file: "badge-96.png", size: 96, design: "badge" },
];

/** Whether (x, y) in [0, 1]² is inside the rounded square. */
function inSquare(x, y, corner) {
  const cx = Math.min(Math.max(x, corner), 1 - corner);
  const cy = Math.min(Math.max(y, corner), 1 - corner);
  return (x - cx) ** 2 + (y - cy) ** 2 <= corner ** 2;
}

/** Whether (x, y) is on the ring or the dot. */
function inMark(x, y, { outer, inner, dot }) {
  const r = Math.hypot(x - 0.5, y - 0.5);
  return (r <= outer && r >= inner) || r <= dot;
}

function draw(size, design) {
  const pixels = Buffer.alloc(size * size * 4);
  for (let py = 0; py < size; py++) {
    for (let px = 0; px < size; px++) {
      // Premultiplied sums over the samples.
      let r = 0;
      let g = 0;
      let b = 0;
      let a = 0;
      for (let sy = 0; sy < SAMPLES; sy++) {
        for (let sx = 0; sx < SAMPLES; sx++) {
          const x = (px + (sx + 0.5) / SAMPLES) / size;
          const y = (py + (sy + 0.5) / SAMPLES) / size;
          const mark = inMark(x, y, design);
          if (design.markOnly ? !mark : !inSquare(x, y, design.corner)) {
            continue;
          }
          const colour = mark ? MARK : BACKGROUND;
          r += colour[0];
          g += colour[1];
          b += colour[2];
          a += 1;
        }
      }
      const offset = (py * size + px) * 4;
      if (a > 0) {
        pixels[offset] = Math.round(r / a);
        pixels[offset + 1] = Math.round(g / a);
        pixels[offset + 2] = Math.round(b / a);
      }
      pixels[offset + 3] = Math.round((a / SAMPLES ** 2) * 255);
    }
  }
  return pixels;
}

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(bytes) {
  let crc = 0xffffffff;
  for (const byte of bytes) crc = CRC_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, crc]);
}

/** An 8-bit RGBA PNG: every scanline unfiltered, then deflated. */
function png(size, pixels) {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8; // bit depth
  header[9] = 6; // RGBA
  const rows = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) {
    pixels.copy(rows, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(rows, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const target = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "public",
  "icons",
);
mkdirSync(target, { recursive: true });
for (const { file, size, design } of ICONS) {
  writeFileSync(join(target, file), png(size, draw(size, DESIGNS[design])));
  console.log(`public/icons/${file} (${size}×${size}, ${design})`);
}
