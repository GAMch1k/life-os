import type { Metadata } from "next";

import { DevicesSettings } from "@/components/devices-settings";
import { SettingsNav } from "@/components/security-log";
import { listDevices } from "@/lib/auth/account-credentials";
import { accountPage } from "@/lib/auth/page-context";
import { userTimeZone } from "@/lib/user-time-zone";

export const metadata: Metadata = { title: "Devices · Life OS" };
export const dynamic = "force-dynamic";

/**
 * Settings → Devices (docs/install/accounts.md#settings--devices): pair a
 * phone with a QR code, and the paired phones and their watches, each with
 * its last use and a way to sign it out. Only with a real sign-in.
 */
export default async function DevicesPage() {
  const { rt, principal, admin } = await accountPage("/settings/devices");
  const [devices, timeZone, legacy] = await Promise.all([
    listDevices(rt, principal.userId),
    userTimeZone(),
    rt.legacy.get(),
  ]);
  return (
    <DevicesSettings
      devices={devices}
      legacyAccess={legacy && principal.role === "owner"}
      nav={<SettingsNav admin={admin} current="/settings/devices" />}
      publicUrl={rt.env.publicUrl}
      timeZone={timeZone}
    />
  );
}
