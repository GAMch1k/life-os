import assert from "node:assert/strict";
import test from "node:test";

import {
  isShareFromApp,
  photoShareError,
  SHARE_ERRORS,
  shareContent,
  shareError,
  shareMetadata,
} from "./share";

test("a share's title, text, and link become one capture, each part once", () => {
  // Chrome: the page title, and its address in url.
  assert.equal(
    shareContent({
      title: "Lasagne al forno",
      text: "",
      url: "https://example.com/lasagne",
    }),
    "Lasagne al forno\nhttps://example.com/lasagne",
  );
  // Instagram: only text, with the link in it.
  assert.equal(
    shareContent({
      text: "Look at this https://www.instagram.com/reel/abc/?igsh=x",
    }),
    "Look at this https://www.instagram.com/reel/abc/?igsh=x",
  );
  // The link also inside the text stays there once; a title repeated in the
  // text is not added again.
  assert.equal(
    shareContent({
      title: "Lasagne",
      text: "Lasagne recipe https://example.com/l",
      url: "https://example.com/l",
    }),
    "Lasagne recipe https://example.com/l",
  );
  // A title that is only the address is the address.
  assert.equal(
    shareContent({
      title: "https://example.com/l",
      url: "https://example.com/l",
    }),
    "https://example.com/l",
  );
  assert.equal(
    shareContent({ title: "  Note ", text: " buy milk \n" }),
    "Note\nbuy milk",
  );
  assert.equal(shareContent({ title: " ", text: "", url: null }), "");
});

test("a shared capture is marked as shared, with the browser's zone when known", () => {
  assert.deepEqual(shareMetadata("Europe/Berlin"), {
    shared: true,
    via: "share-target",
    timeZone: "Europe/Berlin",
  });
  assert.deepEqual(shareMetadata(), { shared: true, via: "share-target" });
});

test("a share is accepted from the app itself, never from another site", () => {
  const from = (headers: Record<string, string>) =>
    isShareFromApp(new Headers(headers));
  // The share sheet: a navigation the browser started, to the app's origin.
  assert.equal(
    from({
      "sec-fetch-site": "none",
      origin: "https://lifeos.gamch1k.com",
      host: "lifeos.gamch1k.com",
    }),
    true,
  );
  assert.equal(from({ "sec-fetch-site": "none", origin: "null" }), true);
  assert.equal(
    from({
      "sec-fetch-site": "same-origin",
      origin: "http://10.66.66.1:3001",
      host: "10.66.66.1",
      "x-forwarded-host": "10.66.66.1",
    }),
    true,
  );
  // Without Sec-Fetch-Site, Origin decides; none at all is the app's own.
  assert.equal(
    from({ origin: "http://localhost:3000", host: "localhost:3000" }),
    true,
  );
  assert.equal(from({}), true);
  // Another site's form, a sibling subdomain, or a foreign Origin.
  assert.equal(
    from({
      "sec-fetch-site": "cross-site",
      origin: "https://evil.example",
      host: "lifeos.gamch1k.com",
    }),
    false,
  );
  assert.equal(
    from({
      "sec-fetch-site": "same-site",
      origin: "https://ntfy.gamch1k.com",
      host: "lifeos.gamch1k.com",
    }),
    false,
  );
  assert.equal(
    from({ origin: "https://evil.example", host: "lifeos.gamch1k.com" }),
    false,
  );
  assert.equal(
    from({ origin: "not a url", host: "lifeos.gamch1k.com" }),
    false,
  );
});

test("a refused photo says why nothing was saved", () => {
  assert.equal(photoShareError("heic_not_supported"), "heic_not_supported");
  assert.equal(photoShareError("image_too_large"), "image_too_large");
  assert.equal(photoShareError(undefined), "inbox");
  assert.equal(photoShareError("internal_error"), "inbox");
  assert.equal(photoShareError("empty"), "inbox");
  assert.match(SHARE_ERRORS.heic_not_supported, /HEIC.*Nothing was saved/);
});

test("only known share errors are shown", () => {
  assert.equal(shareError("inbox"), "inbox");
  assert.equal(shareError(["empty", "inbox"]), "empty");
  assert.equal(shareError("<script>"), undefined);
  assert.equal(shareError("toString"), undefined);
  assert.equal(shareError(undefined), undefined);
});
