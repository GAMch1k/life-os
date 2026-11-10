// Unit tests of the CLA check (cla.js) against a fake GitHub API. No
// dependencies: node --test .github/cla/cla.test.js
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const cla = require("./cla.js");

const { CONFIG } = cla;
const OWNER = "GAMch1k";
const REPO = "life-os";
const SIGN = CONFIG.signPhrase;

const maintainer = { login: "GAMch1k", id: 85905182, type: "User" };
const alice = { login: "alice", id: 1001, type: "User" };
const bob = { login: "bob", id: 1002, type: "User" };
const dependabot = { login: "dependabot[bot]", id: 49699333, type: "Bot" };
const webFlow = { login: "web-flow", id: 19864447, type: "User" };
const actionsBot = { login: CONFIG.botLogin, id: 41898282, type: "Bot" };

function httpError(status, message = `HTTP ${status}`) {
  return Object.assign(new Error(message), { status });
}

function commit(
  sha,
  author,
  { committer = author, name = author ? author.login : "Somebody" } = {},
) {
  return {
    sha,
    author,
    committer,
    commit: { author: { name, email: "hidden@example.invalid" } },
  };
}

// An in-memory GitHub with just the endpoints cla.js uses. `hooks` can make
// a call fail once, to play another run writing at the same time.
function fakeGitHub({
  prAuthor = alice,
  commits = [],
  comments = [],
  signatures,
  branchWithoutFile = false,
  prCommits,
} = {}) {
  const state = {
    pr: {
      number: 7,
      user: prAuthor,
      commits: prCommits ?? commits.length,
      head: { sha: "headsha0000000000000000000000000000000000" },
      base: { repo: { default_branch: "master" } },
    },
    commits,
    comments: comments.map((comment, index) => ({
      id: 500 + index,
      html_url: `https://github.com/${OWNER}/${REPO}/pull/7#issuecomment-${500 + index}`,
      created_at: "2026-11-10T10:00:00Z",
      ...comment,
    })),
    branch:
      signatures !== undefined || branchWithoutFile ? { commits: [] } : null,
    file:
      signatures !== undefined
        ? { sha: "blob-1", content: JSON.stringify({ signatures }) }
        : null,
    statuses: [],
    calls: [],
    hooks: {},
  };
  let blobCounter = 1;
  const call = async (name, params, fn) => {
    state.calls.push({ name, params });
    const hook = state.hooks[name];
    if (hook) {
      delete state.hooks[name];
      await hook(params);
    }
    return fn();
  };
  const putFile = (content, message) => {
    blobCounter += 1;
    state.file = { sha: `blob-${blobCounter}`, content };
    state.branch.commits.push(message);
  };
  const github = {
    rest: {
      pulls: {
        get: (params) => call("pulls.get", params, () => ({ data: state.pr })),
        listCommits: (params) =>
          call("pulls.listCommits", params, () => ({
            data: state.commits.slice(0, CONFIG.maxCommits),
          })),
      },
      issues: {
        listComments: (params) =>
          call("issues.listComments", params, () => ({ data: state.comments })),
        createComment: (params) =>
          call("issues.createComment", params, () => {
            const created = {
              id: 900 + state.comments.length,
              user: actionsBot,
              body: params.body,
              html_url: `https://github.com/${OWNER}/${REPO}/pull/7#issuecomment-900`,
            };
            state.comments.push(created);
            return { data: created };
          }),
        updateComment: (params) =>
          call("issues.updateComment", params, () => {
            const existing = state.comments.find(
              (comment) => comment.id === params.comment_id,
            );
            existing.body = params.body;
            return { data: existing };
          }),
      },
      repos: {
        getContent: (params) =>
          call("repos.getContent", params, () => {
            if (!state.file) throw httpError(404);
            return {
              data: {
                sha: state.file.sha,
                encoding: "base64",
                content: Buffer.from(state.file.content).toString("base64"),
              },
            };
          }),
        createOrUpdateFileContents: (params) =>
          call("repos.createOrUpdateFileContents", params, () => {
            if (!state.branch) throw httpError(404, "Branch not found");
            if (state.file && params.sha !== state.file.sha)
              throw httpError(409, "does not match");
            if (state.file && !params.sha)
              throw httpError(422, '"sha" wasn\'t supplied');
            putFile(
              Buffer.from(params.content, "base64").toString("utf8"),
              params.message,
            );
            return { data: {} };
          }),
        createCommitStatus: (params) =>
          call("repos.createCommitStatus", params, () => {
            state.statuses.push(params);
            return { data: {} };
          }),
      },
      git: {
        getRef: (params) =>
          call("git.getRef", params, () => {
            if (!state.branch) throw httpError(404);
            return { data: {} };
          }),
        getBlob: (params) =>
          call("git.getBlob", params, () => ({
            data: {
              content: Buffer.from(state.file.content).toString("base64"),
            },
          })),
        createTree: (params) =>
          call("git.createTree", params, () => ({
            data: { sha: "tree-1", tree: params.tree },
          })),
        createCommit: (params) =>
          call("git.createCommit", params, () => ({
            data: { sha: "commit-1" },
          })),
        createRef: (params) =>
          call("git.createRef", params, () => {
            if (state.branch) throw httpError(422, "Reference already exists");
            const tree = state.calls.findLast(
              (c) => c.name === "git.createTree",
            ).params.tree;
            const message = state.calls.findLast(
              (c) => c.name === "git.createCommit",
            ).params.message;
            state.branch = { commits: [] };
            putFile(
              tree.find((entry) => entry.path === CONFIG.path).content,
              message,
            );
            return { data: {} };
          }),
      },
    },
    paginate: async (method, params) => (await method(params)).data,
  };
  return { github, state };
}

function core() {
  const lines = [];
  return {
    lines,
    info: (line) => lines.push(line),
    notice: (line) => lines.push(line),
    warning: (line) => lines.push(line),
  };
}

const repo = { owner: OWNER, repo: REPO };
const prEvent = {
  eventName: "pull_request_target",
  repo,
  serverUrl: "https://github.com",
  payload: { action: "synchronize", pull_request: { number: 7 } },
};
function commentEvent(user, body, { onPullRequest = true } = {}) {
  return {
    eventName: "issue_comment",
    repo,
    serverUrl: "https://github.com",
    payload: {
      action: "created",
      issue: {
        number: 7,
        ...(onPullRequest ? { pull_request: { url: "x" } } : {}),
      },
      comment: { id: 999, user, body },
    },
  };
}
const noWait = async () => {};
const storedSignatures = (state) =>
  state.file ? JSON.parse(state.file.content).signatures : [];
const lastStatus = (state) => state.statuses.at(-1);
const botComments = (state) =>
  state.comments.filter((comment) => comment.user.login === CONFIG.botLogin);

test("the sign comment must be the exact sentence, apart from surrounding whitespace", () => {
  assert.equal(cla.isSignComment(SIGN), true);
  assert.equal(cla.isSignComment(`  ${SIGN}\n`), true);
  assert.equal(
    cla.isSignComment(
      "I have read the CLA Document and\nI hereby sign the CLA",
    ),
    true,
  );
  assert.equal(cla.isSignComment(SIGN.toLowerCase()), false);
  assert.equal(cla.isSignComment(`${SIGN}.`), false);
  assert.equal(cla.isSignComment(`> ${SIGN}`), false);
  assert.equal(cla.isSignComment(`Sure! ${SIGN}`), false);
  assert.equal(cla.isSignComment(undefined), false);
  assert.equal(cla.isRecheckComment(" recheck\n"), true);
  assert.equal(cla.isRecheckComment("please recheck"), false);
});

test("the maintainer, web-flow and bots are exempt; others are not", () => {
  assert.equal(cla.isExempt(maintainer), true);
  assert.equal(cla.isExempt({ login: "gamch1k", id: 1, type: "User" }), true);
  assert.equal(cla.isExempt(webFlow), true);
  assert.equal(cla.isExempt(dependabot), true);
  assert.equal(
    cla.isExempt({ login: "renovate[bot]", id: 2, type: "User" }),
    true,
  );
  assert.equal(cla.isExempt({ login: "some-app", id: 3, type: "Bot" }), true);
  assert.equal(cla.isExempt(alice), false);
  assert.equal(cla.isExempt(null), false);
});

test("contributors: the author, commit authors and committers with accounts, once each; unlinked authors apart", () => {
  const pr = { user: alice };
  const commits = [
    commit("a1", alice),
    commit("b1", bob, { committer: webFlow }),
    commit("a2", alice),
    commit("c1", null, { committer: null, name: "Carol" }),
    commit("d1", alice, { committer: null }),
  ];
  const { people, unlinked } = cla.contributors(pr, commits);
  assert.deepEqual(
    people.map((user) => user.login),
    ["alice", "bob", "web-flow"],
  );
  assert.deepEqual(unlinked, [{ sha: "c1", name: "Carol" }]);
});

test("evaluate: signatures count by user id, not by login", () => {
  const pr = { user: { ...alice, login: "alice-renamed" }, commits: 1 };
  const result = cla.evaluate({
    pr,
    commits: [commit("a1", { ...alice, login: "alice-renamed" })],
    signatures: [{ login: "alice", id: alice.id }],
  });
  assert.equal(result.ok, true);
  const impostor = cla.evaluate({
    pr: { user: { login: "alice", id: 4242 }, commits: 1 },
    commits: [commit("x", { login: "alice", id: 4242 })],
    signatures: [{ login: "alice", id: alice.id }],
  });
  assert.equal(impostor.ok, false);
  assert.deepEqual(
    impostor.missing.map((user) => user.id),
    [4242],
  );
});

test("a new pull request by an unsigned contributor fails, with one comment that says how to sign", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice), commit("a2", alice)],
  });
  const result = await cla.run({
    github,
    context: prEvent,
    core: core(),
    wait: noWait,
  });
  assert.equal(result.ok, false);
  const status = lastStatus(state);
  assert.equal(status.context, "CLA");
  assert.equal(status.state, "failure");
  assert.equal(status.sha, state.pr.head.sha);
  assert.match(status.description, /@alice/);
  assert.ok(status.description.length <= 140);
  const comments = botComments(state);
  assert.equal(comments.length, 1);
  assert.ok(comments[0].body.startsWith(CONFIG.marker));
  assert.match(comments[0].body, /@alice/);
  assert.ok(comments[0].body.includes(`\n${SIGN}\n`));
  assert.ok(
    comments[0].body.includes(
      `https://github.com/${OWNER}/${REPO}/blob/master/CLA.md`,
    ),
  );
  assert.equal(status.target_url, comments[0].html_url);
  // Nothing is written until someone signs.
  assert.equal(state.branch, null);
  assert.equal(
    state.calls.filter(
      (c) =>
        c.name.startsWith("git.create") ||
        c.name === "repos.createOrUpdateFileContents",
    ).length,
    0,
  );
});

test("signing records the signature on a new orphan branch, and the check passes", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    comments: [
      { user: alice, body: `${SIGN}\n`, created_at: "2026-11-10T12:34:56Z" },
    ],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  const [{ params: newCommit }] = state.calls.filter(
    (c) => c.name === "git.createCommit",
  );
  assert.deepEqual(newCommit.parents, []);
  const tree = state.calls.find((c) => c.name === "git.createTree").params;
  assert.equal(tree.base_tree, undefined);
  assert.deepEqual(tree.tree.map((entry) => entry.path).sort(), [
    "README.md",
    "signatures/cla.json",
  ]);
  assert.equal(
    state.calls.find((c) => c.name === "git.createRef").params.ref,
    "refs/heads/cla-signatures",
  );
  assert.deepEqual(storedSignatures(state), [
    {
      login: "alice",
      id: alice.id,
      version: CONFIG.version,
      commentUrl: `https://github.com/${OWNER}/${REPO}/pull/7#issuecomment-500`,
      pullRequest: 7,
      repository: `${OWNER}/${REPO}`,
      signedAt: "2026-11-10T12:34:56.000Z",
    },
  ]);
  assert.doesNotMatch(newCommit.message, /#/);
  assert.equal(lastStatus(state).state, "success");
  // Nothing was missing in the end, so no comment.
  assert.equal(botComments(state).length, 0);
});

test("the sign comment event signs its author and turns the existing comment into a thank-you", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [{ login: "zoe", id: 5 }],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.equal(lastStatus(state).state, "failure");
  state.comments.push({
    id: 600,
    user: alice,
    body: SIGN,
    html_url: "https://github.com/c/600",
    created_at: "2026-11-10T13:00:00Z",
  });
  await cla.run({
    github,
    context: commentEvent(alice, SIGN),
    core: core(),
    wait: noWait,
  });
  assert.deepEqual(
    storedSignatures(state).map((signature) => signature.id),
    [5, alice.id],
  );
  assert.equal(state.calls.filter((c) => c.name === "git.createRef").length, 0);
  assert.equal(lastStatus(state).state, "success");
  const comments = botComments(state);
  assert.equal(comments.length, 1);
  assert.match(comments[0].body, /everyone in this pull request has signed/);
  assert.ok(comments[0].body.startsWith(CONFIG.marker));
});

test("one person can't sign for another", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
    comments: [{ user: bob, body: SIGN }],
  });
  await cla.run({
    github,
    context: commentEvent(bob, SIGN),
    core: core(),
    wait: noWait,
  });
  assert.deepEqual(storedSignatures(state), []);
  assert.equal(lastStatus(state).state, "failure");
  assert.match(lastStatus(state).description, /@alice/);
});

test("every committer with an account and the pull request author must sign", async () => {
  const { github, state } = fakeGitHub({
    prAuthor: bob,
    commits: [commit("a1", alice, { committer: webFlow })],
    signatures: [{ login: "alice", id: alice.id }],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.equal(lastStatus(state).state, "failure");
  assert.match(lastStatus(state).description, /@bob/);
  assert.doesNotMatch(lastStatus(state).description, /alice|web-flow/);
});

test("the maintainer's and Dependabot's pull requests pass without a comment", async () => {
  for (const author of [maintainer, dependabot]) {
    const { github, state } = fakeGitHub({
      prAuthor: author,
      commits: [commit("m1", author, { committer: webFlow })],
    });
    await cla.run({ github, context: prEvent, core: core(), wait: noWait });
    assert.equal(lastStatus(state).state, "success");
    assert.equal(botComments(state).length, 0);
    assert.equal(state.branch, null);
  }
});

test("a commit whose author has no GitHub account fails, naming the commit but not the email", async () => {
  const { github, state } = fakeGitHub({
    commits: [
      commit("a1", alice),
      commit("c0ffee1234", null, {
        committer: null,
        name: "Carol `@everyone`",
      }),
    ],
    signatures: [{ login: "alice", id: alice.id }],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.equal(lastStatus(state).state, "failure");
  assert.match(lastStatus(state).description, /no GitHub account/);
  const [comment] = botComments(state);
  assert.match(comment.body, /c0ffee1 by `Carol @everyone`/);
  assert.match(comment.body, /linked to any GitHub account/);
  assert.doesNotMatch(comment.body, /example\.invalid/);
  assert.doesNotMatch(comment.body, /Still to sign/);
});

test("more commits than the API lists fails", async () => {
  const { github, state } = fakeGitHub({
    prAuthor: maintainer,
    commits: [commit("m1", maintainer)],
    prCommits: CONFIG.maxCommits + 1,
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.equal(lastStatus(state).state, "failure");
  assert.match(botComments(state)[0].body, /Too many commits/);
});

test("a signature a cancelled run missed is picked up by the next run", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
    comments: [{ user: alice, body: SIGN }],
  });
  await cla.run({
    github,
    context: commentEvent(bob, "recheck"),
    core: core(),
    wait: noWait,
  });
  assert.deepEqual(
    storedSignatures(state).map((signature) => signature.login),
    ["alice"],
  );
  assert.equal(lastStatus(state).state, "success");
});

test("recheck from anyone re-evaluates and updates the comment only when it changed", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  await cla.run({
    github,
    context: commentEvent(bob, "recheck"),
    core: core(),
    wait: noWait,
  });
  assert.equal(state.statuses.length, 2);
  assert.equal(
    state.calls.filter((c) => c.name === "issues.createComment").length,
    1,
  );
  assert.equal(
    state.calls.filter((c) => c.name === "issues.updateComment").length,
    0,
  );
});

test("a comment that only contains a marker is not taken for ours", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
    comments: [{ user: alice, body: `${CONFIG.marker} fake` }],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.equal(
    state.calls.filter((c) => c.name === "issues.updateComment").length,
    0,
  );
  assert.equal(botComments(state).length, 1);
});

test("comments on issues, other comments, bot comments and edits do nothing", async () => {
  const cases = [
    commentEvent(alice, SIGN, { onPullRequest: false }),
    commentEvent(alice, "Looks good, but please recheck the tests"),
    commentEvent(actionsBot, SIGN),
    {
      ...commentEvent(alice, SIGN),
      payload: { ...commentEvent(alice, SIGN).payload, action: "edited" },
    },
    { eventName: "push", repo, payload: {} },
  ];
  for (const context of cases) {
    const { github, state } = fakeGitHub({ commits: [commit("a1", alice)] });
    assert.equal(
      await cla.run({ github, context, core: core(), wait: noWait }),
      null,
    );
    assert.equal(state.calls.length, 0);
  }
});

test("a write that loses a race (409) reads again and keeps the other signature", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
    comments: [{ user: alice, body: SIGN }],
  });
  state.hooks["repos.createOrUpdateFileContents"] = () => {
    state.file = {
      sha: "blob-other",
      content: JSON.stringify({ signatures: [{ login: "zoe", id: 5 }] }),
    };
  };
  const waits = [];
  await cla.run({
    github,
    context: prEvent,
    core: core(),
    wait: async (ms) => waits.push(ms),
  });
  assert.equal(waits.length, 1);
  assert.deepEqual(
    storedSignatures(state).map((signature) => signature.id),
    [5, alice.id],
  );
  assert.equal(lastStatus(state).state, "success");
});

test("a branch another run created first (422) is appended to", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    comments: [{ user: alice, body: SIGN }],
  });
  state.hooks["git.createRef"] = () => {
    state.branch = { commits: ["other run"] };
    state.file = {
      sha: "blob-other",
      content: JSON.stringify({ signatures: [{ login: "zoe", id: 5 }] }),
    };
  };
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  assert.deepEqual(
    storedSignatures(state).map((signature) => signature.id),
    [5, alice.id],
  );
  assert.equal(lastStatus(state).state, "success");
});

test("the branch without the file gets the file", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    branchWithoutFile: true,
    comments: [{ user: alice, body: SIGN }],
  });
  await cla.run({ github, context: prEvent, core: core(), wait: noWait });
  const put = state.calls.find(
    (c) => c.name === "repos.createOrUpdateFileContents",
  ).params;
  assert.equal(put.branch, "cla-signatures");
  assert.equal(put.sha, undefined);
  assert.equal(storedSignatures(state)[0].id, alice.id);
});

test("a write that keeps failing gives up, and other errors are not retried", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
    comments: [{ user: alice, body: SIGN }],
  });
  let conflicts = 0;
  const original = github.rest.repos.createOrUpdateFileContents;
  github.rest.repos.createOrUpdateFileContents = async () => {
    conflicts += 1;
    throw httpError(409);
  };
  await assert.rejects(
    cla.run({ github, context: prEvent, core: core(), wait: noWait }),
    { status: 409 },
  );
  assert.equal(conflicts, 5);
  github.rest.repos.createOrUpdateFileContents = async () => {
    throw httpError(403, "Resource not accessible by integration");
  };
  await assert.rejects(
    cla.run({ github, context: prEvent, core: core(), wait: noWait }),
    { status: 403 },
  );
  github.rest.repos.createOrUpdateFileContents = original;
  // No status was set: the required check stays missing, so nothing merges.
  assert.equal(state.statuses.length, 0);
});

test("a broken signatures file fails the run instead of being overwritten", async () => {
  const { github, state } = fakeGitHub({
    commits: [commit("a1", alice)],
    signatures: [],
  });
  state.file.content = '{"oops": true}';
  await assert.rejects(
    cla.run({ github, context: prEvent, core: core(), wait: noWait }),
    /no "signatures" list/,
  );
  assert.equal(state.statuses.length, 0);
});

test("the status description stays within 140 characters", () => {
  const many = Array.from({ length: 30 }, (_, index) => ({
    login: `contributor-number-${index}`,
    id: index,
  }));
  const description = cla.statusDescription({
    ok: false,
    missing: many,
    unlinked: [],
    tooMany: false,
  });
  assert.ok(description.length <= 140);
});

test("CLA.md shows the version the check records, and the sentence", () => {
  const document = fs.readFileSync(
    path.join(__dirname, "..", "..", CONFIG.document),
    "utf8",
  );
  assert.ok(
    document.includes(`Version ${CONFIG.version},`),
    `CLA.md must say "Version ${CONFIG.version}, <date>"`,
  );
  assert.ok(document.includes(SIGN));
});

test("the workflow never checks out or runs the pull request's code", () => {
  const workflow = fs.readFileSync(
    path.join(__dirname, "..", "workflows", "cla.yml"),
    "utf8",
  );
  const code = workflow
    .split("\n")
    .filter((line) => !line.trim().startsWith("#"))
    .join("\n");
  assert.match(code, /^permissions: \{\}$/m);
  // A ref (such as the head's SHA) on actions/checkout would fetch the
  // pull request's code into a job that holds a write token.
  assert.doesNotMatch(code, /^\s*ref:/m);
  assert.doesNotMatch(code, /\.head\b|head_ref|head\.sha/);
  assert.doesNotMatch(code, /^\s*run:/m);
  const actions = [...code.matchAll(/^\s*(?:- )?uses:\s*(\S+)/gm)].map(
    (match) => match[1],
  );
  assert.ok(actions.length > 0);
  for (const action of actions) {
    assert.match(
      action,
      /^actions\/[\w-]+@[0-9a-f]{40}$/,
      `${action} must be an official action pinned to a commit SHA`,
    );
  }
});
