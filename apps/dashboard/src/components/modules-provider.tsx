"use client";

import { createContext, useContext } from "react";

import {
  ALL_PARTS,
  type DashboardFeature,
  type InstalledParts,
} from "@/lib/modules";

const ModulesContext = createContext<InstalledParts>(ALL_PARTS);

/**
 * The parts of the Dashboard the modules that are on bring
 * (src/lib/modules.ts), for the client components: the root layout reads
 * them from LIFE_OS_MODULES on the server. Without a provider, every part
 * is on.
 */
export function ModulesProvider({
  children,
  parts,
}: Readonly<{ children: React.ReactNode; parts: InstalledParts }>) {
  return (
    <ModulesContext.Provider value={parts}>{children}</ModulesContext.Provider>
  );
}

export function useInstalled(): InstalledParts {
  return useContext(ModulesContext);
}

/** Whether a module that is on brings `feature`. */
export function useFeature(feature: DashboardFeature): boolean {
  return useContext(ModulesContext).features.includes(feature);
}
