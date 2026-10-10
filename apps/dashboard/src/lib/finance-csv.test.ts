import assert from "node:assert/strict";
import { test } from "node:test";

import { csvFileProblem, MAX_CSV_BYTES } from "./finance-csv";

test("csvFileProblem accepts a .csv file up to the service limit", () => {
  assert.equal(csvFileProblem({ name: "bank.CSV", size: 10 }), undefined);
  assert.equal(
    csvFileProblem({ name: "bank.csv", size: MAX_CSV_BYTES }),
    undefined,
  );
});

test("csvFileProblem explains other files and oversized CSVs", () => {
  assert.equal(
    csvFileProblem({ name: "bank.xlsx", size: 10 }),
    "Choose a .csv file.",
  );
  assert.equal(
    csvFileProblem({ name: "bank.csv", size: MAX_CSV_BYTES + 1 }),
    "This CSV is over 4 MB. Split it into smaller files.",
  );
});
