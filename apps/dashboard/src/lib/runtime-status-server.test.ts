import assert from "node:assert/strict";
import test from "node:test";

import {
  SERVER_CONTAINERS_ACTION,
  SERVER_DISK_ACTION,
  SERVER_LOAD_ACTION,
  SERVER_MEMORY_ACTION,
  formatBinaryBytes,
  statusAlerts,
  toRuntimeStatus,
  type HostHistory,
  type HostStatus,
  type RuntimeSignals,
  type StatusCheck,
} from "./runtime-status";

const now = new Date("2026-10-11T12:00:00Z");
const GiB = 1024 ** 3;
const MiB = 1024 ** 2;

type Finding = HostStatus["findings"][number];

/** A healthy 7.7 GiB, 6-CPU server, as host-health reports it. */
const healthy = (overrides: Partial<HostStatus> = {}): HostStatus => ({
  sampledAt: now.toISOString(),
  mode: "host",
  memory: {
    totalBytes: 8_238_493_696,
    availableBytes: 3.6 * GiB,
    availablePercent: 46.9,
  },
  load: { one: 0.93, five: 2.21, fifteen: 1.3, cpus: 6 },
  uptimeSeconds: 12 * 86400 + 5 * 3600,
  disks: [
    {
      label: "root",
      totalBytes: 232 * GiB,
      freeBytes: 125 * GiB,
      freePercent: 53.9,
    },
  ],
  containers: {
    available: true,
    total: 32,
    running: 28,
    unhealthy: 0,
    restarting: 0,
    items: [],
    top: [
      { name: "lifeos-whisper-1", project: "lifeos", memoryBytes: 1.9 * GiB },
      {
        name: "homeassistant",
        project: "homeassistant",
        memoryBytes: 600 * MiB,
      },
      { name: "n8n", project: "n8n", memoryBytes: 420 * MiB },
      { name: "portainer", memoryBytes: 60 * MiB },
    ],
  },
  thresholds: {
    memoryWarningPercent: 15,
    memoryWarningSamples: 3,
    memoryProblemPercent: 7,
    diskWarningBytes: 10 * GiB,
    diskWarningPercent: 10,
    diskProblemBytes: 3 * GiB,
    loadWarningPerCpu: 1,
    restartLoopCount: 3,
    restartLoopMinutes: 15,
    unhealthyMinutes: 5,
  },
  findings: [],
  ...overrides,
});

/** A day of samples, one a minute, memory between 3.4 and 4.4 GiB. */
const day = (): HostHistory => ({
  intervalSeconds: 60,
  samples: Array.from({ length: 1440 }, (_, minute) => ({
    at: new Date(now.getTime() - (1439 - minute) * 60_000).toISOString(),
    memoryAvailableBytes: Math.round(
      (3.9 + 0.5 * Math.sin((minute / 1440) * 2 * Math.PI)) * GiB,
    ),
    memoryTotalBytes: 8_238_493_696,
    load1: 1,
    load5: 1,
    load15: 1,
    disks: [{ label: "root", freeBytes: 125 * GiB }],
  })),
});

const signals = (
  server: RuntimeSignals["server"],
  hostHealthReady = true,
): RuntimeSignals => ({
  processor: null,
  inbox: { pendingCount: 0, retryingCount: 0, humanReviewCount: 0 },
  voice: {
    queuedCount: 0,
    processingCount: 0,
    retryableCount: 0,
    workerStalled: false,
    whisperReady: true,
  },
  server,
  services: [
    { name: "Tasks", ready: true },
    { name: "Host health", ready: hostHealthReady },
  ],
});

function serverCheck(
  status: HostStatus | null | undefined,
  history: HostHistory | null = day(),
  hostHealthReady = true,
): StatusCheck | undefined {
  const server =
    status === undefined
      ? undefined
      : status === null
        ? null
        : { status, history };
  return toRuntimeStatus(
    signals(server, hostHealthReady),
    now,
    "Europe/Berlin",
  ).checks.find((check) => check.id === "server");
}

const fact = (check: StatusCheck | undefined, label: string) =>
  check?.facts.find((item) => item.label === label)?.value;

test("a healthy server is OK, with its memory, load, disks, containers, and uptime", () => {
  const check = serverCheck(healthy());
  assert.equal(check?.state, "ok");
  assert.equal(check?.title, "Server");
  assert.equal(
    check?.summary,
    "Running normally: 3.6 GiB of memory available, a 15-minute load of 1.3 on 6 CPUs.",
  );
  assert.equal(fact(check, "Memory"), "3.6 GiB available of 7.7 GiB (47%)");
  assert.equal(
    fact(check, "Load"),
    "0.93, 2.21, 1.30 (1, 5, 15 min) on 6 CPUs",
  );
  assert.equal(fact(check, "Disk root"), "125 GiB free of 232 GiB (54%)");
  assert.equal(
    fact(check, "Containers"),
    "28 running, 0 unhealthy, 0 restarting (32 in all)",
  );
  assert.equal(
    fact(check, "Most memory"),
    "lifeos-whisper-1 1.9 GiB, homeassistant 600 MiB, n8n 420 MiB",
  );
  assert.equal(fact(check, "Up for"), "12 days");
  assert.equal(fact(check, "Source"), undefined);
  assert.equal(check?.action, undefined);
});

test("the card draws memory available over the day", () => {
  const chart = serverCheck(healthy())?.chart;
  assert.ok(chart);
  assert.equal(chart.label, "Memory available, last 24 h");
  assert.equal(chart.max, 8_238_493_696);
  assert.equal(chart.threshold, (8_238_493_696 * 15) / 100);
  assert.ok(
    chart.points.length <= 96 && chart.points.length >= 90,
    `${chart.points.length} points`,
  );
  assert.equal(chart.points.at(-1)?.at, now.toISOString());
  // Each point is its span's lowest sample, so dips are never smoothed away.
  assert.ok(Math.min(...chart.points.map((point) => point.value)) < 3.41 * GiB);
  // Without a history (just restarted) there is no chart.
  assert.equal(serverCheck(healthy(), null)?.chart, undefined);
  assert.equal(
    serverCheck(healthy(), {
      intervalSeconds: 60,
      samples: day().samples.slice(-1),
    })?.chart,
    undefined,
  );
});

test("without host-health the check is left out, and a stopped one is one alert", () => {
  assert.equal(serverCheck(undefined), undefined);
  assert.equal(serverCheck(null, null, false), undefined);
  const unreadable = serverCheck(null, null, true);
  assert.equal(unreadable?.state, "problem");
  assert.equal(unreadable?.summary, "The server's health cannot be read.");
});

const memory = (severity: Finding["severity"], available: number): Finding => ({
  kind: "memory_low",
  severity,
  availableBytes: available,
  totalBytes: 8_238_493_696,
  minutes: 4,
});

test("low memory names the biggest users and says what to do", () => {
  const warning = serverCheck(
    healthy({ findings: [memory("warning", 1.1 * GiB)] }),
  );
  assert.equal(warning?.state, "warning");
  assert.equal(
    warning?.summary,
    "Memory is low: 1.1 GiB available of 7.7 GiB. Biggest: lifeos-whisper-1 1.9 GiB, homeassistant 600 MiB, n8n 420 MiB.",
  );
  assert.equal(warning?.action, SERVER_MEMORY_ACTION);
  const problem = serverCheck(
    healthy({ findings: [memory("problem", 520 * MiB)] }),
  );
  assert.equal(problem?.state, "problem");
  assert.match(
    problem?.summary ?? "",
    /^Memory is very low: 520 MiB available of 7\.7 GiB\. Biggest: lifeos-whisper-1 1\.9 GiB/,
  );
  // Without container facts the summary still says how low.
  const alone = serverCheck(
    healthy({
      findings: [memory("problem", 520 * MiB)],
      containers: { ...healthy().containers, top: [] },
    }),
  );
  assert.equal(
    alone?.summary,
    "Memory is very low: 520 MiB available of 7.7 GiB.",
  );
});

test("disks, load, and containers each say what is wrong and what to do", () => {
  const cases: [Finding, StatusCheck["state"], string, string][] = [
    [
      {
        kind: "disk_low",
        severity: "warning",
        subject: "root",
        availableBytes: 8.2 * GiB,
        totalBytes: 232 * GiB,
      },
      "warning",
      "The root disk is filling up: 8.2 GiB free of 232 GiB.",
      SERVER_DISK_ACTION,
    ],
    [
      {
        kind: "disk_low",
        severity: "problem",
        subject: "docker",
        availableBytes: 2 * GiB,
        totalBytes: 500 * GiB,
      },
      "problem",
      "The docker disk is almost full: 2.0 GiB free of 500 GiB.",
      SERVER_DISK_ACTION,
    ],
    [
      { kind: "load_high", severity: "warning", load: 7.42, cpus: 6 },
      "warning",
      "The server is busy: its 15-minute load is 7.4 on 6 CPUs.",
      SERVER_LOAD_ACTION,
    ],
    [
      {
        kind: "container_restarting",
        severity: "warning",
        subject: "homeassistant",
        project: "homeassistant",
        count: 4,
        minutes: 15,
      },
      "warning",
      "homeassistant keeps restarting: 4 times in 15 min.",
      "See why with docker logs homeassistant.",
    ],
    [
      {
        kind: "container_restarting",
        severity: "warning",
        subject: "runner",
        project: "forgejo",
        count: 3,
        minutes: 15,
      },
      "warning",
      "runner (forgejo) keeps restarting: 3 times in 15 min.",
      "See why with docker logs runner.",
    ],
    [
      {
        kind: "container_unhealthy",
        severity: "warning",
        subject: "n8n",
        project: "n8n",
        minutes: 12,
      },
      "warning",
      "n8n has been unhealthy for 12 min.",
      "See its health check with docker inspect --format '{{json .State.Health}}' n8n.",
    ],
    [
      {
        kind: "containers_unavailable",
        severity: "warning",
        error:
          "the docker gateway answered 502: the docker socket refused the gateway",
      },
      "warning",
      "The containers cannot be read: the docker gateway answered 502: the docker socket refused the gateway.",
      SERVER_CONTAINERS_ACTION,
    ],
  ];
  for (const [finding, state, summary, action] of cases) {
    const check = serverCheck(healthy({ findings: [finding] }));
    assert.equal(check?.state, state, summary);
    assert.equal(check?.summary, summary);
    assert.equal(check?.action, action);
  }
  const unavailable = serverCheck(
    healthy({
      findings: [cases[6][0]],
      containers: {
        available: false,
        error: "unreachable",
        total: 0,
        running: 0,
        unhealthy: 0,
        restarting: 0,
        items: [],
        top: [],
      },
    }),
  );
  assert.equal(fact(unavailable, "Containers"), "not available");
  assert.equal(fact(unavailable, "Most memory"), undefined);
});

test("several findings: the first (the worst) leads, the others are listed", () => {
  const check = serverCheck(
    healthy({
      findings: [
        {
          kind: "disk_low",
          severity: "problem",
          subject: "root",
          availableBytes: 2 * GiB,
          totalBytes: 232 * GiB,
        },
        memory("warning", 1.1 * GiB),
        { kind: "load_high", severity: "warning", load: 7, cpus: 6 },
      ],
    }),
  );
  assert.equal(check?.state, "problem");
  assert.equal(
    check?.summary,
    "The root disk is almost full: 2.0 GiB free of 232 GiB.",
  );
  assert.equal(check?.facts[0].label, "Also");
  assert.match(
    fact(check, "Also") ?? "",
    /^Memory is low: 1\.1 GiB available of 7\.7 GiB\. Biggest: .* The server is busy: its 15-minute load is 7\.0 on 6 CPUs\.$/,
  );
  assert.ok(
    statusAlerts(
      toRuntimeStatus(
        signals({
          status: healthy({ findings: [memory("problem", 400 * MiB)] }),
          history: null,
        }),
        now,
      ),
    ).some((alert) => alert.id === "server" && alert.state === "problem"),
  );
});

test("a made-up server says so", () => {
  assert.equal(
    fact(serverCheck(healthy({ mode: "fixture" })), "Source"),
    "a made-up server (HOST_HEALTH_FIXTURE)",
  );
});

test("formatBinaryBytes counts in powers of 1024, as free -h and df -h", () => {
  assert.equal(formatBinaryBytes(0), "0 B");
  assert.equal(formatBinaryBytes(1023), "1023 B");
  assert.equal(formatBinaryBytes(520 * MiB), "520 MiB");
  assert.equal(formatBinaryBytes(8_238_493_696), "7.7 GiB");
  assert.equal(formatBinaryBytes(232 * GiB), "232 GiB");
  assert.equal(formatBinaryBytes(1.5 * 1024 * GiB), "1.5 TiB");
});
