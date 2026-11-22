// The README's images from the screenshots tools/screenshots took
// (docs/development/screenshots.md): each <name>.png, taken at twice its
// size, becomes a smaller <name>.webp next to it, and social-preview.png is
// compressed in place (GitHub takes a PNG under 1 MB for the repository's
// social preview). Sharp comes with Next.js's dependencies.
//
//   node e2e-screenshots/optimize.mjs <screenshots-dir>
import { readdir, stat } from "node:fs/promises";
import path from "node:path";

import sharp from "sharp";

const [dir] = process.argv.slice(2);
if (!dir) {
  console.error("usage: node e2e-screenshots/optimize.mjs <screenshots-dir>");
  process.exit(2);
}

// The widths the README shows them at, crisp on a high-density screen.
const DESKTOP_WIDTH = 1600;
const PHONE_WIDTH = 585;
const QUALITY = 80;

const kb = async (file) => `${Math.round((await stat(file)).size / 1024)} KB`;

let total = 0;
for (const file of (await readdir(dir)).sort()) {
  if (!file.endsWith(".png")) continue;
  const source = path.join(dir, file);
  const name = file.slice(0, -".png".length);
  if (name === "social-preview") {
    const image = await sharp(source)
      .png({ compressionLevel: 9, effort: 10 })
      .toBuffer();
    await sharp(image).toFile(source);
    console.log(`${file}: ${await kb(source)}`);
    continue;
  }
  const target = path.join(dir, `${name}.webp`);
  await sharp(source)
    .resize({
      width: name.startsWith("phone-") ? PHONE_WIDTH : DESKTOP_WIDTH,
      withoutEnlargement: true,
    })
    .webp({ quality: QUALITY, effort: 6 })
    .toFile(target);
  total += (await stat(target)).size;
  console.log(`${name}.webp: ${await kb(target)}`);
}
console.log(`The WebP files: ${Math.round(total / 1024)} KB in all`);
