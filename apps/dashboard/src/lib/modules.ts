import { MODULES } from "@/config/modules.generated";

/**
 * Life OS's modules as the Dashboard sees them (docs/architecture/modules.md).
 * The module manifests (modules/*.yaml) say which of the Dashboard's parts
 * each module brings: its navigation entries, the services it probes and
 * calls, its Status checks, and its features, such as the voice recorder.
 * The Dashboard asks about those parts, never about a module by name, so a
 * part shows exactly while a module that brings it is on.
 *
 * Which modules are on comes from LIFE_OS_MODULES, which compose.yaml sets
 * from COMPOSE_PROFILES: the core always, and each optional module it lists.
 */
export type Module = (typeof MODULES)[number];
export type ModuleId = Module["id"];
export type DashboardFeature = Module["dashboard"]["features"][number];
export type DashboardService = Module["dashboard"]["services"][number];
export type StatusCheckId = Module["dashboard"]["checks"][number];
export type NavHref = Module["dashboard"]["nav"][number];

/** The parts of the Dashboard that the modules that are on bring. */
export type InstalledParts = {
  modules: ModuleId[];
  nav: NavHref[];
  services: DashboardService[];
  checks: StatusCheckId[];
  features: DashboardFeature[];
};

/**
 * The modules a LIFE_OS_MODULES value turns on: the core, and each optional
 * module the comma-separated list names (unknown names are left out).
 * Unset, every module: a Dashboard started without it (development, or an
 * older compose.yaml) shows everything, as before modules.
 */
export function enabledModules(value: string | undefined): Module[] {
  if (value === undefined) return [...MODULES];
  const listed = new Set(
    value
      .split(",")
      .map((name) => name.trim())
      .filter(Boolean),
  );
  return MODULES.filter(
    (entry) => entry.install === "core" || listed.has(entry.id),
  );
}

/** What the modules a LIFE_OS_MODULES value turns on bring. */
export function installedParts(value: string | undefined): InstalledParts {
  const modules = enabledModules(value);
  return {
    modules: modules.map((entry) => entry.id),
    nav: modules.flatMap((entry) => [...entry.dashboard.nav]),
    services: modules.flatMap((entry) => [...entry.dashboard.services]),
    checks: modules.flatMap((entry) => [...entry.dashboard.checks]),
    features: modules.flatMap((entry) => [...entry.dashboard.features]),
  };
}

/** Every part: as the Dashboard is without LIFE_OS_MODULES. */
export const ALL_PARTS = installedParts(undefined);

/** This Dashboard's parts, from its LIFE_OS_MODULES (server side). */
export function installed(): InstalledParts {
  return installedParts(process.env.LIFE_OS_MODULES);
}

export function featureOn(
  feature: DashboardFeature,
  parts: InstalledParts = installed(),
): boolean {
  return parts.features.includes(feature);
}

export function serviceOn(
  service: DashboardService,
  parts: InstalledParts = installed(),
): boolean {
  return parts.services.includes(service);
}

export function checkOn(
  check: StatusCheckId,
  parts: InstalledParts = installed(),
): boolean {
  return parts.checks.includes(check);
}

/**
 * The answer to a call for a service whose module is off, like the
 * gateway's for such a module's routes: 404 module_off, at once.
 */
export function moduleOffResponse(): Response {
  return Response.json(
    {
      error: {
        code: "module_off",
        message: "This Life OS module is turned off",
      },
    },
    { status: 404 },
  );
}
