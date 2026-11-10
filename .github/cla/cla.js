// The Contributor License Agreement check (X-13), run by
// .github/workflows/cla.yml through actions/github-script:
//
//   const cla = require('./.github/cla/cla.js');
//   await cla.run({ github, context, core });
//
// It reads the pull request only through the API (its commits' authors and
// its comments) and never runs anything from it. Everyone who wrote a commit
// in the pull request, and its author, must have signed CLA.md once, unless
// allowlisted. The result is the commit status `CLA` on the pull request's
// head commit, and one comment on the pull request, edited in place.
//
// Signatures live in signatures/cla.json on the cla-signatures branch, by
// GitHub user id: a login can change, the id can't. A contributor signs by
// commenting CONFIG.signPhrase; every run also picks up signature comments a
// cancelled or failed run missed, so a signature is never lost.
//
// The same file is in GAMch1k/life-os and GAMch1k/life-os-app; keep them
// equal. Tests: node --test .github/cla/cla.test.js
"use strict";

const CONFIG = Object.freeze({
  // CLA.md's version. A test checks that CLA.md shows it.
  version: "1.0",
  document: "CLA.md",
  branch: "cla-signatures",
  path: "signatures/cla.json",
  // The commit status; the rulesets require it by this name.
  context: "CLA",
  // The sentence the archived contributor-assistant/github-action used, so
  // contributors who signed elsewhere know it.
  signPhrase: "I have read the CLA Document and I hereby sign the CLA",
  recheckPhrase: "recheck",
  // Finds our comment among the pull request's comments.
  marker: "<!-- life-os-cla -->",
  // The workflow's GITHUB_TOKEN comments as this user.
  botLogin: "github-actions[bot]",
  // Logins that never have to sign (compared case-insensitively). Bots, any
  // login ending in [bot], need no entry.
  allowlist: Object.freeze([
    "GAMch1k", // the maintainer
    "web-flow", // GitHub itself: the committer of commits made on github.com
  ]),
  // The REST API lists at most 250 commits of a pull request.
  maxCommits: 250,
});

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Whitespace at the ends and line breaks don't matter; case does.
function normalize(body) {
  return String(body ?? "")
    .trim()
    .replace(/\s+/g, " ");
}

function isSignComment(body) {
  return normalize(body) === CONFIG.signPhrase;
}

function isRecheckComment(body) {
  return normalize(body) === CONFIG.recheckPhrase;
}

function isExempt(user, allowlist = CONFIG.allowlist) {
  if (!user || !user.login) return false;
  if (user.type === "Bot" || user.login.endsWith("[bot]")) return true;
  const login = user.login.toLowerCase();
  return allowlist.some((entry) => entry.toLowerCase() === login);
}

// Who has to sign: the pull request's author, and each commit's author and
// committer that is a GitHub account. A commit whose author is no account
// (its email is linked to none) is listed apart: nobody can sign for it. A
// committer without an account is left out; it is usually the author
// rebasing, or GitHub.
function contributors(pr, commits) {
  const people = new Map();
  const unlinked = [];
  const add = (user) => {
    if (user && user.id != null && !people.has(user.id))
      people.set(user.id, { id: user.id, login: user.login, type: user.type });
  };
  add(pr.user);
  for (const commit of commits) {
    if (commit.author && commit.author.id != null) add(commit.author);
    else
      unlinked.push({
        sha: commit.sha,
        name: commit.commit?.author?.name || "unknown",
      });
    add(commit.committer);
  }
  return { people: [...people.values()], unlinked };
}

function evaluate({ pr, commits, signatures, allowlist = CONFIG.allowlist }) {
  const { people, unlinked } = contributors(pr, commits);
  const signed = new Set(signatures.map((signature) => signature.id));
  const missing = people.filter(
    (user) => !isExempt(user, allowlist) && !signed.has(user.id),
  );
  // pulls.listCommits stops at 250: the rest can't be checked.
  const tooMany = (pr.commits ?? commits.length) > commits.length;
  return {
    missing,
    unlinked,
    tooMany,
    ok: missing.length === 0 && unlinked.length === 0 && !tooMany,
  };
}

// Signature comments of the people who still have to sign. A comment signs
// only for its own author; the first such comment counts.
function signaturesFromComments(
  comments,
  missing,
  { repository, pullRequest, version = CONFIG.version },
) {
  const wanted = new Set(missing.map((user) => user.id));
  const records = new Map();
  for (const comment of comments) {
    const user = comment.user;
    if (
      !user ||
      !wanted.has(user.id) ||
      records.has(user.id) ||
      !isSignComment(comment.body)
    )
      continue;
    records.set(user.id, {
      login: user.login,
      id: user.id,
      version,
      commentUrl: comment.html_url,
      pullRequest,
      repository,
      signedAt: new Date(comment.created_at).toISOString(),
    });
  }
  return [...records.values()];
}

function serialize(signatures) {
  return `${JSON.stringify({ signatures }, null, 2)}\n`;
}

function branchReadme(repository, defaultBranch) {
  return (
    `This branch holds the signatures of the Life OS Contributor License Agreement ` +
    `([CLA.md](https://github.com/${repository}/blob/${defaultBranch}/CLA.md)), in signatures/cla.json, ` +
    `recorded by the CLA workflow (.github/workflows/cla.yml) from pull request comments.\n`
  );
}

// The signatures, and what the next write needs: whether the branch exists,
// and the file's blob SHA (the contents API's guard against lost updates).
async function readStore(github, { owner, repo }) {
  try {
    const { data } = await github.rest.repos.getContent({
      owner,
      repo,
      path: CONFIG.path,
      ref: CONFIG.branch,
    });
    let content = data.content;
    if (data.encoding === "none" || !content) {
      // Over 1 MB the contents API leaves the content out.
      content = (
        await github.rest.git.getBlob({ owner, repo, file_sha: data.sha })
      ).data.content;
    }
    const parsed = JSON.parse(Buffer.from(content, "base64").toString("utf8"));
    if (!parsed || !Array.isArray(parsed.signatures)) {
      throw new Error(
        `${CONFIG.path} on ${CONFIG.branch} has no "signatures" list; fix the file by hand`,
      );
    }
    return { branch: true, sha: data.sha, signatures: parsed.signatures };
  } catch (error) {
    if (error.status !== 404) throw error;
  }
  try {
    await github.rest.git.getRef({
      owner,
      repo,
      ref: `heads/${CONFIG.branch}`,
    });
    return { branch: true, sha: null, signatures: [] };
  } catch (error) {
    if (error.status !== 404) throw error;
    return { branch: false, sha: null, signatures: [] };
  }
}

// The branch starts as an orphan: no history of the code, only the README
// and the signatures.
async function createBranch(
  github,
  { owner, repo, defaultBranch },
  content,
  message,
) {
  const { data: tree } = await github.rest.git.createTree({
    owner,
    repo,
    tree: [
      {
        path: "README.md",
        mode: "100644",
        type: "blob",
        content: branchReadme(`${owner}/${repo}`, defaultBranch),
      },
      { path: CONFIG.path, mode: "100644", type: "blob", content },
    ],
  });
  const { data: commit } = await github.rest.git.createCommit({
    owner,
    repo,
    message,
    tree: tree.sha,
    parents: [],
  });
  await github.rest.git.createRef({
    owner,
    repo,
    ref: `refs/heads/${CONFIG.branch}`,
    sha: commit.sha,
  });
}

// Appends the records that are not there yet. Another run may write at the
// same time: the contents API then answers 409 (the file changed since we
// read it) or 422 (the file or branch appeared), and we read again and add
// to what is there now.
async function recordSignatures(
  github,
  { owner, repo, defaultBranch },
  records,
  { attempts = 5, wait = sleep } = {},
) {
  for (let attempt = 1; ; attempt++) {
    const store = await readStore(github, { owner, repo });
    const known = new Set(store.signatures.map((signature) => signature.id));
    const added = records.filter((record) => !known.has(record.id));
    if (added.length === 0) return store.signatures;
    const signatures = [...store.signatures, ...added];
    const content = serialize(signatures);
    // No "#123": that would add a reference to the pull request's timeline.
    const message = `Record the CLA signature of ${added.map((record) => record.login).join(", ")} (pull request ${added[0].pullRequest})`;
    try {
      if (store.branch) {
        await github.rest.repos.createOrUpdateFileContents({
          owner,
          repo,
          branch: CONFIG.branch,
          path: CONFIG.path,
          message,
          content: Buffer.from(content, "utf8").toString("base64"),
          ...(store.sha ? { sha: store.sha } : {}),
        });
      } else {
        await createBranch(
          github,
          { owner, repo, defaultBranch },
          content,
          message,
        );
      }
      return signatures;
    } catch (error) {
      if (![409, 422].includes(error.status) || attempt >= attempts)
        throw error;
      await wait(attempt * 1000 + Math.floor(Math.random() * 1000));
    }
  }
}

// A commit author's name is free text: shown as code, so it can't mention
// anyone or add a link.
function codeSpan(text) {
  const clean = String(text)
    .replace(/[`\r\n]/g, "")
    .slice(0, 60);
  return `\`${clean || "unknown"}\``;
}

function commentBody(result, claUrl) {
  if (result.ok) {
    return `${CONFIG.marker}\n**CLA:** everyone in this pull request has signed the [Contributor License Agreement](${claUrl}). Thank you!\n`;
  }
  const lines = [
    CONFIG.marker,
    `Thank you for your pull request! Before it can be merged, everyone who wrote a commit in it needs to sign the Life OS [Contributor License Agreement](${claUrl}) once. You keep the copyright in your work, and it stays open source under the AGPL; the agreement also lets the maintainer offer Life OS under other licenses.`,
  ];
  if (result.missing.length > 0) {
    lines.push(
      "",
      `**Still to sign:** ${result.missing.map((user) => `@${user.login}`).join(", ")}. To sign, post a comment on this pull request with exactly this sentence:`,
      "",
      "```",
      CONFIG.signPhrase,
      "```",
    );
  }
  if (result.unlinked.length > 0) {
    const list = result.unlinked
      .map((commit) => `${commit.sha.slice(0, 7)} by ${codeSpan(commit.name)}`)
      .join(", ");
    lines.push(
      "",
      `**Commits without a GitHub account:** ${list}. Their author email isn't linked to any GitHub account, so the check can't tell who has to sign. Add that email to your GitHub account (Settings › Emails), or give the commits an author email that is linked (for example \`git commit --amend --reset-author\`, or a rebase), and push again.`,
    );
  }
  if (result.tooMany) {
    lines.push(
      "",
      `**Too many commits:** the check can read only the first ${CONFIG.maxCommits} commits of a pull request. Please squash some of them.`,
    );
  }
  lines.push(
    "",
    "The check runs again on every push. To run it again by hand, comment `recheck`.",
  );
  return `${lines.join("\n")}\n`;
}

function statusDescription(result) {
  let text;
  if (result.ok) text = "Everyone has signed the CLA";
  else if (result.missing.length > 0)
    text = `Waiting for the CLA signature of ${result.missing.map((user) => `@${user.login}`).join(", ")}`;
  else if (result.unlinked.length > 0)
    text = "A commit author has no GitHub account; see the comment";
  else text = `More than ${CONFIG.maxCommits} commits; see the comment`;
  // GitHub's limit for a status description.
  return text.length > 140 ? `${text.slice(0, 139)}…` : text;
}

// Our comment, edited in place; created only when something is missing, so a
// pull request that needs nothing gets no comment.
async function upsertComment(
  github,
  { owner, repo, number },
  comments,
  body,
  { create },
) {
  const existing = comments.find(
    (comment) =>
      comment.user?.login === CONFIG.botLogin &&
      String(comment.body ?? "").includes(CONFIG.marker),
  );
  if (existing) {
    if (existing.body !== body)
      await github.rest.issues.updateComment({
        owner,
        repo,
        comment_id: existing.id,
        body,
      });
    return existing.html_url;
  }
  if (!create) return null;
  const { data } = await github.rest.issues.createComment({
    owner,
    repo,
    issue_number: number,
    body,
  });
  return data.html_url;
}

async function run({ github, context, core, wait = sleep }) {
  const { owner, repo } = context.repo;
  let number;
  let comment = null;
  if (context.eventName === "pull_request_target") {
    number = context.payload.pull_request.number;
  } else if (context.eventName === "issue_comment") {
    comment = context.payload.comment;
    const issue = context.payload.issue;
    if (context.payload.action !== "created" || !issue?.pull_request) {
      core.info("Not a new comment on a pull request: nothing to do.");
      return null;
    }
    if (comment?.user?.type === "Bot") {
      core.info("A comment by a bot: nothing to do.");
      return null;
    }
    if (!isSignComment(comment.body) && !isRecheckComment(comment.body)) {
      core.info('Neither a signature nor "recheck": nothing to do.');
      return null;
    }
    number = issue.number;
  } else {
    core.info(`The CLA check doesn't handle ${context.eventName} events.`);
    return null;
  }

  const { data: pr } = await github.rest.pulls.get({
    owner,
    repo,
    pull_number: number,
  });
  const defaultBranch =
    pr.base?.repo?.default_branch || context.payload.repository?.default_branch;
  const commits = await github.paginate(github.rest.pulls.listCommits, {
    owner,
    repo,
    pull_number: number,
    per_page: 100,
  });
  const comments = await github.paginate(github.rest.issues.listComments, {
    owner,
    repo,
    issue_number: number,
    per_page: 100,
  });

  let { signatures } = await readStore(github, { owner, repo });
  let result = evaluate({ pr, commits, signatures });

  const records = signaturesFromComments(comments, result.missing, {
    repository: `${owner}/${repo}`,
    pullRequest: number,
  });
  if (records.length > 0) {
    signatures = await recordSignatures(
      github,
      { owner, repo, defaultBranch },
      records,
      { wait },
    );
    result = evaluate({ pr, commits, signatures });
    core.info(
      `Recorded the signature of ${records.map((record) => `${record.login} (${record.id})`).join(", ")}.`,
    );
  }
  if (
    comment &&
    isSignComment(comment.body) &&
    !records.some((record) => record.id === comment.user.id)
  ) {
    core.info(
      `${comment.user.login} signed before, needn't sign, or wrote no commit in this pull request: nothing recorded for them.`,
    );
  }

  const serverUrl = context.serverUrl || "https://github.com";
  const claUrl = `${serverUrl}/${owner}/${repo}/blob/${defaultBranch}/${CONFIG.document}`;
  const commentUrl = await upsertComment(
    github,
    { owner, repo, number },
    comments,
    commentBody(result, claUrl),
    { create: !result.ok },
  );
  const description = statusDescription(result);
  await github.rest.repos.createCommitStatus({
    owner,
    repo,
    sha: pr.head.sha,
    state: result.ok ? "success" : "failure",
    context: CONFIG.context,
    description,
    target_url: commentUrl || claUrl,
  });
  core.info(
    `${CONFIG.context} on ${pr.head.sha}: ${result.ok ? "success" : "failure"} (${description}).`,
  );
  return result;
}

module.exports = {
  CONFIG,
  normalize,
  isSignComment,
  isRecheckComment,
  isExempt,
  contributors,
  evaluate,
  signaturesFromComments,
  readStore,
  recordSignatures,
  commentBody,
  statusDescription,
  run,
};
