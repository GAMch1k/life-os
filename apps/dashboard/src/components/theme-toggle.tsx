"use client";

import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";

import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";

const THEMES = [
  { value: "light", label: "Light theme", icon: Sun },
  { value: "dark", label: "Dark theme", icon: Moon },
  { value: "system", label: "System theme", icon: Monitor },
] as const;

const subscribe = () => () => {};

/**
 * False on the server and during hydration, true afterwards. The chosen theme
 * lives in localStorage, so it is only known in the browser; rendering it
 * before hydration would not match the server HTML.
 */
function useHydrated() {
  return useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );
}

export function ThemeToggle() {
  const { theme, setTheme } = useTheme();
  const hydrated = useHydrated();

  return (
    // A segmented control: the chosen theme raised on a muted track.
    <ToggleGroup
      aria-label="Theme"
      className="bg-muted p-0.5"
      onValueChange={(value: string[]) => {
        // Pressing the active theme again would clear the group; keep it.
        if (value[0]) setTheme(value[0]);
      }}
      size="sm"
      spacing={0.5}
      value={hydrated && theme ? [theme] : []}
    >
      {THEMES.map(({ icon: Icon, label, value }) => (
        <ToggleGroupItem
          aria-label={label}
          className="text-muted-foreground aria-pressed:bg-background aria-pressed:text-foreground px-0 aria-pressed:shadow-sm"
          key={value}
          title={label}
          value={value}
        >
          <Icon aria-hidden="true" />
        </ToggleGroupItem>
      ))}
    </ToggleGroup>
  );
}
