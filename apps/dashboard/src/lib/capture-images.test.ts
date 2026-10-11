import assert from "node:assert/strict";
import test from "node:test";

import createClient from "openapi-fetch";

import type { paths } from "./api/generated/inbox";
import {
  captureImages,
  imageErrorMessage,
  MAX_IMAGE_BYTES,
  mediaUrl,
  photosContent,
  photosProblem,
  uploadImages,
} from "./capture-images";

const hash = (char: string) => char.repeat(64);

test("a capture's photos load from Inbox's media endpoint behind the gateway", () => {
  assert.equal(
    mediaUrl(`inbox-media:${hash("a")}`),
    `/api/inbox/v1/media/${hash("a")}`,
  );
  assert.equal(
    mediaUrl(`inbox-media:${hash("a")}`, "http://inbox:8080"),
    `http://inbox:8080/v1/media/${hash("a")}`,
  );
  for (const reference of [
    "https://example.com/a.jpg",
    `inbox-media:${hash("A")}`,
    "inbox-media:../../secret",
    hash("a"),
  ]) {
    assert.equal(mediaUrl(reference), undefined, reference);
  }
  assert.deepEqual(
    captureImages({
      mediaReferences: [
        `inbox-media:${hash("a")}`,
        "https://example.com/a.jpg",
        `inbox-media:${hash("b")}`,
      ],
    }),
    [`inbox-media:${hash("a")}`, `inbox-media:${hash("b")}`],
  );
  assert.deepEqual(captureImages({}), []);
});

test("a capture of photos only says how many", () => {
  assert.equal(photosContent(1), "Photo");
  assert.equal(photosContent(3), "3 photos");
});

test("photos are checked before uploading, as Inbox will", () => {
  const photo = (name: string, size = 1000, type = "image/jpeg") => ({
    name,
    size,
    type,
  });
  assert.equal(photosProblem([photo("a.jpg"), photo("b.png")]), undefined);
  assert.equal(photosProblem([photo("a.jpg")], 4), undefined);
  assert.equal(
    photosProblem([photo("a.jpg"), photo("b.jpg")], 4),
    "too_many_images",
  );
  assert.equal(
    photosProblem([photo("IMG_1.HEIC", 1000, "image/heic")]),
    "heic_not_supported",
  );
  // Some browsers give a HEIC photo no type: its name tells.
  assert.equal(
    photosProblem([photo("IMG_1.heic", 1000, "")]),
    "heic_not_supported",
  );
  assert.equal(
    photosProblem([photo("notes.pdf", 1000, "application/pdf")]),
    "unsupported_image",
  );
  assert.equal(
    photosProblem([photo("big.jpg", MAX_IMAGE_BYTES + 1)]),
    "image_too_large",
  );
  assert.equal(photosProblem([photo("empty.jpg", 0)]), "image_too_large");
  assert.match(imageErrorMessage("heic_not_supported"), /HEIC/);
  assert.match(imageErrorMessage("image_too_large"), /10 MB/);
  assert.match(imageErrorMessage("something else"), /try again/);
});

type Sent = { url: string; files: string[]; actor: string | null };

function fakeInbox(answer: (index: number) => Response) {
  const sent: Sent[] = [];
  const client = createClient<paths>({
    baseUrl: "http://inbox.test",
    headers: { "X-Life-OS-Actor": "user" },
    fetch: async (request: Request) => {
      const form = await request.formData();
      sent.push({
        url: request.url,
        files: form.getAll("images").map((file) => (file as File).name),
        actor: request.headers.get("X-Life-OS-Actor"),
      });
      return answer(sent.length - 1);
    },
  });
  return { client, sent };
}

const stored = (...hashes: string[]) =>
  Response.json(
    {
      items: hashes.map((value) => ({
        reference: `inbox-media:${value}`,
        sha256: value,
        contentType: "image/jpeg",
        byteSize: 3,
      })),
    },
    { status: 201 },
  );

test("photos are uploaded one per request from the browser, in order", async () => {
  const { client, sent } = fakeInbox((index) => stored(hash(String(index))));
  const files = ["a.jpg", "b.jpg", "c.jpg"].map(
    (name) => new File(["img"], name, { type: "image/jpeg" }),
  );
  const result = await uploadImages(files, { client });
  assert.deepEqual(result, {
    references: [0, 1, 2].map((index) => `inbox-media:${hash(String(index))}`),
  });
  assert.deepEqual(
    sent.map(({ url, files, actor }) => [url, files, actor]),
    [
      ["http://inbox.test/v1/media", ["a.jpg"], "user"],
      ["http://inbox.test/v1/media", ["b.jpg"], "user"],
      ["http://inbox.test/v1/media", ["c.jpg"], "user"],
    ],
  );
});

test("a share's photos go in one request, and a refusal names Inbox's reason", async () => {
  const files = ["a.jpg", "b.jpg"].map((name) => new File(["img"], name));
  const batched = fakeInbox(() => stored(hash("a"), hash("b")));
  const result = await uploadImages(files, {
    client: batched.client,
    perRequest: 5,
  });
  assert.deepEqual(result, {
    references: [`inbox-media:${hash("a")}`, `inbox-media:${hash("b")}`],
  });
  assert.deepEqual(batched.sent[0].files, ["a.jpg", "b.jpg"]);

  const refused = fakeInbox(() =>
    Response.json(
      { error: { code: "heic_not_supported", message: "HEIC" } },
      { status: 400 },
    ),
  );
  assert.deepEqual(await uploadImages(files, { client: refused.client }), {
    code: "heic_not_supported",
  });
  const down = fakeInbox(() => {
    throw new TypeError("fetch failed");
  });
  assert.deepEqual(await uploadImages(files, { client: down.client }), {});
});
