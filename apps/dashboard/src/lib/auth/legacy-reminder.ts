import { DEVICE_PAIRING_AVAILABLE } from "./constants";

/**
 * Whether the Dashboard shows the daily reminder that legacy access is on
 * (docs/install/accounts.md#legacy-access): only once devices can be paired
 * (DEVICE_PAIRING_AVAILABLE, P21-06), since turning legacy access off before
 * that cuts off the phone and watch apps; then while legacy access is on,
 * an account exists, and the reminder was not hidden today.
 */
export function showsLegacyReminder(
  account: { legacyAccess: boolean; accountExists: boolean } | undefined,
  hiddenToday: boolean,
  pairingAvailable: boolean = DEVICE_PAIRING_AVAILABLE,
): boolean {
  return (
    pairingAvailable &&
    account?.legacyAccess === true &&
    account.accountExists &&
    !hiddenToday
  );
}
