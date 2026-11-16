import assert from "node:assert/strict";
import test from "node:test";

import { keyName } from "./keys";

test("a device's name is cleaned as the phone app cleans it", () => {
  assert.equal(keyName(" Pixel  9 "), "Pixel 9");
  // White space, a line or paragraph separator included, becomes a space.
  assert.equal(keyName("Pixel\t9\nPro X Y"), "Pixel 9 Pro X Y");
  // Control and format characters are dropped.
  assert.equal(keyName("bell\u0007 phone​‮﻿"), "bell phone");
  assert.equal(keyName("a\u0085b\u009fc"), "abc");
  // Emoji stay whole: each UTF-16 unit is judged on its own, as the app
  // does, and a surrogate is no format character. A zero-width joiner is.
  assert.equal(keyName("Anna's 📱"), "Anna's 📱");
  assert.equal(keyName("👩‍💻"), "👩💻");
  // At most 64 UTF-16 units, never half a surrogate pair.
  assert.equal(keyName("x".repeat(65)), "x".repeat(64));
  const cut = keyName(`${"x".repeat(63)}📱`);
  assert.equal(cut, "x".repeat(63));
  assert.equal(keyName(`${"x".repeat(62)}📱`), `${"x".repeat(62)}📱`);
  assert.equal(keyName(`${"x".repeat(63)} y`), "x".repeat(63));
  // Nothing left is no name.
  for (const empty of ["", "   ", "\u0007​", "\n\t", null, 7, undefined]) {
    assert.equal(keyName(empty), null, JSON.stringify(empty));
  }
});
