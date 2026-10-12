"use client";

import { Sparkles } from "lucide-react";
import {
  type FormEvent,
  useEffect,
  useId,
  useState,
  useTransition,
} from "react";

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
import { Skeleton } from "@/components/ui/skeleton";
import { financeClient } from "@/lib/api/finance-client";
import {
  type PaydayDraft,
  type PaydayErrors,
  type PaydaySettings,
  fetchPayday,
  offeredSuggestion,
  paydayBody,
  paydayDraft,
  suggestionLabel,
  suggestionNote,
} from "@/lib/finance-payday";
import { type Loaded, financeErrorMessage } from "@/lib/finance-workspace";
import { useBrowserLocale } from "@/lib/use-browser-locale";

/**
 * Payday on the Budget tab: switched on, a booked payment from the payer
 * (as the bank statement names it) asks the AI to plan the budget for the
 * month the money is for, with a notification when the plan is ready.
 * Finance suggests the payer from the income that arrives monthly; it is
 * used only when the owner picks it and saves. The setting is read when the
 * dialog opens.
 */
export function PaydayDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent>
        {open ? <Payday onClose={onClose} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function Payday({ onClose }: { onClose: () => void }) {
  const [settings, setSettings] = useState<Loaded<PaydaySettings>>();

  useEffect(() => {
    let cancelled = false;
    void fetchPayday().then((result) => {
      if (!cancelled) setSettings(result);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  if (settings?.data) {
    return (
      <PaydayForm
        onCancel={onClose}
        onSaved={onClose}
        settings={settings.data}
      />
    );
  }
  return (
    <>
      <DialogHeader>
        <DialogTitle>Payday</DialogTitle>
        <DialogDescription role={settings ? "alert" : undefined}>
          {settings?.error ?? "Loading the setting…"}
        </DialogDescription>
      </DialogHeader>
      {settings ? null : <Skeleton className="h-28 w-full" />}
      <DialogFooter>
        <Button onClick={onClose} type="button" variant="outline">
          Close
        </Button>
      </DialogFooter>
    </>
  );
}

function PaydayForm({
  settings,
  onCancel,
  onSaved,
}: {
  settings: PaydaySettings;
  onCancel: () => void;
  onSaved: (settings: PaydaySettings) => void;
}) {
  const id = useId();
  const locale = useBrowserLocale();
  const [draft, setDraft] = useState<PaydayDraft>(() =>
    paydayDraft(settings, locale),
  );
  const [errors, setErrors] = useState<PaydayErrors & { form?: string }>({});
  const [pending, startTransition] = useTransition();
  const update = (patch: Partial<PaydayDraft>) =>
    setDraft((value) => ({ ...value, ...patch }));
  const invalid = (field: keyof PaydayErrors) =>
    errors[field]
      ? { "aria-invalid": true, "aria-describedby": `${id}-${field}-error` }
      : {};
  const suggestion = offeredSuggestion(settings.suggestion, draft);

  const submit = (event: FormEvent) => {
    event.preventDefault();
    const { body, errors: found } = paydayBody(draft, settings.currency);
    if (!body) return setErrors(found);
    startTransition(async () => {
      setErrors({});
      const { data, error } = await financeClient
        .PUT("/v1/settings/payday", { body })
        .catch(() => ({ data: undefined, error: undefined }));
      if (!data) {
        return setErrors({
          form: financeErrorMessage(error, "Could not save the payday."),
        });
      }
      onSaved(data);
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <DialogHeader>
        <DialogTitle>Payday</DialogTitle>
        <DialogDescription>
          When your salary is booked, the AI plans the budget for the month it
          is for (the next month from the 15th on), and you get a notification
          when the plan is ready. Nothing changes until you apply it.
        </DialogDescription>
      </DialogHeader>

      <FieldGroup>
        <Label className="w-fit">
          <Checkbox
            checked={draft.enabled}
            onCheckedChange={(checked) => update({ enabled: checked })}
          />
          Plan when my salary arrives
        </Label>

        <Field data-invalid={Boolean(errors.payer)}>
          <FieldLabel htmlFor={`${id}-payer`}>Paid by</FieldLabel>
          <Input
            disabled={!draft.enabled}
            id={`${id}-payer`}
            onChange={(event) => update({ payer: event.target.value })}
            placeholder="As your bank statement names it"
            value={draft.payer}
            {...invalid("payer")}
          />
          {suggestion ? (
            <div
              className="flex flex-wrap items-center gap-x-2 gap-y-1"
              data-testid="finance-payday-suggestion"
            >
              <Button
                onClick={() =>
                  update({ enabled: true, payer: suggestion.payerMatch })
                }
                size="xs"
                type="button"
                variant="secondary"
              >
                <Sparkles aria-hidden="true" />
                {suggestionLabel(suggestion)}
              </Button>
              <span className="text-muted-foreground text-xs">
                {suggestionNote(suggestion, locale)}
              </span>
            </div>
          ) : null}
          <FieldDescription className="text-xs">
            A payment counts when its sender contains this text, ignoring case.
          </FieldDescription>
          <FieldError id={`${id}-payer-error`}>{errors.payer}</FieldError>
        </Field>

        <Field data-invalid={Boolean(errors.minimum)}>
          <FieldLabel htmlFor={`${id}-minimum`}>
            Smallest salary (optional)
          </FieldLabel>
          <div className="relative">
            <Input
              className="pr-12"
              disabled={!draft.enabled}
              id={`${id}-minimum`}
              inputMode="decimal"
              onChange={(event) => update({ minimum: event.target.value })}
              placeholder="0.00"
              value={draft.minimum}
              {...invalid("minimum")}
            />
            <span className="text-muted-foreground pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-xs">
              {settings.currency}
            </span>
          </div>
          <FieldDescription className="text-xs">
            Smaller payments from them, such as refunds, don&apos;t count. Leave
            it empty for any amount.
          </FieldDescription>
          <FieldError id={`${id}-minimum-error`}>{errors.minimum}</FieldError>
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
          {pending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}
