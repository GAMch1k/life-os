"use client";

import { Toggle } from "@base-ui/react/toggle";
import { ToggleGroup } from "@base-ui/react/toggle-group";
import { Monitor, Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";
import { useSyncExternalStore } from "react";

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
    <ToggleGroup
      aria-label="Theme"
      className="bg-muted flex items-center gap-0.5 rounded-lg p-0.5"
      onValueChange={(value) => {
        // Pressing the active theme again would clear the group; keep it.
        if (value[0]) setTheme(value[0]);
      }}
      value={hydrated && theme ? [theme] : []}
    >
      {THEMES.map(({ icon: Icon, label, value }) => (
        <Toggle
          aria-label={label}
          className="text-muted-foreground hover:text-foreground focus-visible:ring-ring data-pressed:bg-background data-pressed:text-foreground flex size-7 items-center justify-center rounded-md transition-colors outline-none focus-visible:ring-2 data-pressed:shadow-sm"
          key={value}
          title={label}
          value={value}
        >
          <Icon aria-hidden="true" className="size-4" />
        </Toggle>
      ))}
    </ToggleGroup>
  );
}
