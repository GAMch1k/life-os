"use client";

import { type FormEvent, useId, useState, useTransition } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { financeClient } from "@/lib/api/finance-client";
import {
  type FinanceSettings,
  MAX_LEAD_DAYS,
  type SettingsDraft,
  type SettingsErrors,
  settingsBody,
  settingsDraft,
} from "@/lib/finance-subscriptions";
import { type Loaded, financeErrorMessage } from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";

/**
 * The Alerts settings: notifications for large debits and low balances
 * (thresholds in the default currency) and renewal reminders (how many days
 * ahead), each with its own switch.
 */
export function AlertsDialog({
  open,
  settings,
  onClose,
  onSaved,
}: {
  open: boolean;
  settings: Loaded<FinanceSettings>;
  onClose: () => void;
  onSaved: (settings: FinanceSettings) => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent className="sm:max-w-md">
        {settings.error !== undefined ? (
          <>
            <DialogHeader>
              <DialogTitle>Alerts</DialogTitle>
              <DialogDescription>{settings.error}</DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button onClick={onClose} type="button" variant="outline">
                Close
              </Button>
            </DialogFooter>
          </>
        ) : (
          <AlertsForm
            key={settings.data.updatedAt}
            onCancel={onClose}
            onSaved={(saved) => {
              onSaved(saved);
              onClose();
            }}
            settings={settings.data}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function AlertsForm({
  settings,
  onCancel,
  onSaved,
}: {
  settings: FinanceSettings;
  onCancel: () => void;
  onSaved: (settings: FinanceSettings) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const [draft, setDraft] = useState<SettingsDraft>(() =>
    settingsDraft(settings, locale),
  );
  const [errors, setErrors] = useState<SettingsErrors & { form?: string }>({});
  const [pending, startTransition] = useTransition();
  const update = (patch: Partial<SettingsDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));
  const invalid = (field: keyof SettingsErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { body, errors: found } = settingsBody(draft, settings.currency);
    if (!body) return setErrors(found);
    startTransition(async () => {
      setErrors({});
      const { data, error } = await financeClient
        .PUT("/v1/settings", { body })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not save the alerts."),
        });
      }
      onSaved(data);
    });
  };

  const currency = (
    <span className="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">
      {settings.currency}
    </span>
  );

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Alerts</DialogTitle>
        <DialogDescription>
          Notifications about your money. Amounts are in {settings.currency};
          accounts in other currencies are not checked.
        </DialogDescription>
      </DialogHeader>

      <FieldGroup>
        <Field data-invalid={Boolean(errors.largeDebit)}>
          <Label className="w-fit">
            <Checkbox
              checked={draft.largeDebitEnabled}
              onCheckedChange={(checked) =>
                update({ largeDebitEnabled: checked })
              }
            />
            Large debits
          </Label>
          <FieldLabel
            className="text-muted-foreground text-xs font-normal"
            htmlFor={`${id}-large`}
          >
            <span className="sr-only">Large debits</span> from
          </FieldLabel>
          <div className="relative">
            <Input
              className="pr-12"
              disabled={!draft.largeDebitEnabled}
              id={`${id}-large`}
              inputMode="decimal"
              onChange={(event) => update({ largeDebit: event.target.value })}
              value={draft.largeDebit}
              {...invalid("largeDebit")}
            />
            {currency}
          </div>
          <FieldDescription className="text-xs">
            A notification for each payment of at least this amount, booked or
            pending.
          </FieldDescription>
          <FieldError id={`${id}-largeDebit-error`}>
            {errors.largeDebit}
          </FieldError>
        </Field>

        <Field data-invalid={Boolean(errors.lowBalance)}>
          <Label className="w-fit">
            <Checkbox
              checked={draft.lowBalanceEnabled}
              onCheckedChange={(checked) =>
                update({ lowBalanceEnabled: checked })
              }
            />
            Low balance
          </Label>
          <FieldLabel
            className="text-muted-foreground text-xs font-normal"
            htmlFor={`${id}-low`}
          >
            <span className="sr-only">Low balance</span> below
          </FieldLabel>
          <div className="relative">
            <Input
              className="pr-12"
              disabled={!draft.lowBalanceEnabled}
              id={`${id}-low`}
              inputMode="decimal"
              onChange={(event) => update({ lowBalance: event.target.value })}
              value={draft.lowBalance}
              {...invalid("lowBalance")}
            />
            {currency}
          </div>
          <FieldDescription className="text-xs">
            A notification when a bank account falls below this amount, and
            again after it went back above.
          </FieldDescription>
          <FieldError id={`${id}-lowBalance-error`}>
            {errors.lowBalance}
          </FieldError>
        </Field>

        <Field data-invalid={Boolean(errors.leadDays)}>
          <Label className="w-fit">
            <Checkbox
              checked={draft.renewalsEnabled}
              onCheckedChange={(checked) =>
                update({ renewalsEnabled: checked })
              }
            />
            Renewal reminders
          </Label>
          <FieldLabel
            className="text-muted-foreground text-xs font-normal"
            htmlFor={`${id}-lead`}
          >
            <span className="sr-only">Renewal reminders,</span> remind me
          </FieldLabel>
          <div className="relative">
            <Input
              className="pr-24"
              disabled={!draft.renewalsEnabled}
              id={`${id}-lead`}
              inputMode="numeric"
              onChange={(event) => update({ leadDays: event.target.value })}
              value={draft.leadDays}
              {...invalid("leadDays")}
            />
            <span className="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">
              days before
            </span>
          </div>
          <FieldDescription className="text-xs">
            A quiet reminder before a confirmed subscription renews (0 is on the
            day, at most {MAX_LEAD_DAYS}).
          </FieldDescription>
          <FieldError id={`${id}-leadDays-error`}>{errors.leadDays}</FieldError>
        </Field>
      </FieldGroup>

      {errors.form ? (
        <p className="text-destructive text-sm" role="alert">
          {errors.form}
        </p>
      ) : null}

      <DialogFooter>
        <Button
          disabled={pending}
          onClick={onCancel}
          type="button"
          variant="outline"
        >
          Cancel
        </Button>
        <Button disabled={pending} type="submit">
          {pending ? "Saving…" : "Save alerts"}
        </Button>
      </DialogFooter>
    </form>
  );
}
