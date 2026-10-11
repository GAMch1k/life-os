"use client";

import { X } from "lucide-react";
import { type FormEvent, useEffect, useId, useState } from "react";

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/app-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Field,
  FieldDescription,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { foodClient } from "@/lib/api/food-client";
import { load, writeHeader } from "@/lib/food";
import { COOKING_METHODS, type CookingMethod } from "@/lib/food-recipes";
import {
  type PreferencesDraft,
  addDislike,
  fetchPreferences,
  preferencesBody,
  preferencesDraft,
} from "@/lib/food-suggest";

/**
 * The cooking preferences: the preferred ways of cooking (the air fryer to
 * start with), ingredients never to suggest, and notes for whoever suggests
 * recipes. They are read when the dialog opens.
 */
export function PreferencesDialog({
  open,
  onClose,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  return (
    <Dialog
      onOpenChange={(next) => {
        if (!next) onClose();
      }}
      open={open}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Cooking preferences</DialogTitle>
          <DialogDescription>
            Suggestions favour the ways you like to cook and leave out what you
            do not eat.
          </DialogDescription>
        </DialogHeader>
        {open ? <PreferencesLoader onSaved={onSaved} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function PreferencesLoader({ onSaved }: { onSaved: () => void }) {
  const [state, setState] = useState<
    | { kind: "loading" }
    | { kind: "error"; text: string }
    | { kind: "ready"; draft: PreferencesDraft }
  >({ kind: "loading" });
  useEffect(() => {
    let live = true;
    void fetchPreferences().then((result) => {
      if (!live) return;
      setState(
        result.error !== undefined
          ? { kind: "error", text: result.error }
          : { kind: "ready", draft: preferencesDraft(result.data) },
      );
    });
    return () => {
      live = false;
    };
  }, []);
  if (state.kind === "loading")
    return <p className="text-muted-foreground text-sm">Loading…</p>;
  if (state.kind === "error")
    return <p className="text-destructive text-sm">{state.text}</p>;
  return <PreferencesForm initial={state.draft} onSaved={onSaved} />;
}

function PreferencesForm({
  initial,
  onSaved,
}: {
  initial: PreferencesDraft;
  onSaved: () => void;
}) {
  const id = useId();
  const [draft, setDraft] = useState(initial);
  const [dislike, setDislike] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string>();

  const add = () => {
    setDraft((value) => ({
      ...value,
      dislikes: addDislike(value.dislikes, dislike),
    }));
    setDislike("");
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    // A name typed but not added yet counts.
    const body = preferencesBody({
      ...draft,
      dislikes: addDislike(draft.dislikes, dislike),
    });
    setPending(true);
    setError(undefined);
    void load(
      () =>
        foodClient.PUT("/v1/preferences", {
          params: { header: writeHeader() },
          body,
        }),
      "Could not save the preferences.",
    ).then((result) => {
      setPending(false);
      if (result.error !== undefined) return setError(result.error);
      onSaved();
    });
  };

  return (
    <form className="grid gap-4" noValidate onSubmit={submit}>
      <FieldSet>
        <FieldLegend variant="label">Preferred ways of cooking</FieldLegend>
        <ToggleGroup
          aria-label="Preferred ways of cooking"
          className="flex-wrap"
          multiple
          onValueChange={(value) =>
            setDraft((current) => ({
              ...current,
              methods: value as CookingMethod[],
            }))
          }
          size="lg"
          spacing={1}
          value={draft.methods}
          variant="outline"
        >
          {COOKING_METHODS.map((method) => (
            <ToggleGroupItem key={method.value} value={method.value}>
              {method.label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <FieldDescription>
          Recipes cooked these ways rank higher.
        </FieldDescription>
      </FieldSet>

      <Field>
        <FieldLabel htmlFor={`${id}-dislike`}>Never suggest</FieldLabel>
        {draft.dislikes.length ? (
          <ul className="flex flex-wrap gap-1.5" data-testid="food-dislikes">
            {draft.dislikes.map((name) => (
              <li key={name}>
                <Badge className="gap-1 pr-0.5" variant="secondary">
                  {name}
                  <Button
                    aria-label={`Remove ${name}`}
                    className="size-6"
                    onClick={() =>
                      setDraft((current) => ({
                        ...current,
                        dislikes: current.dislikes.filter(
                          (entry) => entry !== name,
                        ),
                      }))
                    }
                    size="icon-sm"
                    type="button"
                    variant="ghost"
                  >
                    <X aria-hidden="true" />
                  </Button>
                </Badge>
              </li>
            ))}
          </ul>
        ) : null}
        <div className="flex gap-2">
          <Input
            className="h-10 flex-1 text-base"
            id={`${id}-dislike`}
            maxLength={200}
            onChange={(event) => setDislike(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                add();
              }
            }}
            placeholder="Koriander, Rosinen"
            value={dislike}
          />
          <Button
            disabled={!dislike.trim()}
            onClick={add}
            size="lg"
            type="button"
            variant="outline"
          >
            Add
          </Button>
        </div>
        <FieldDescription>
          Recipes with these ingredients are left out.
        </FieldDescription>
      </Field>

      <Field>
        <FieldLabel htmlFor={`${id}-notes`}>Notes</FieldLabel>
        <Textarea
          id={`${id}-notes`}
          maxLength={1000}
          onChange={(event) =>
            setDraft((current) => ({
              ...current,
              dietaryNotes: event.target.value,
            }))
          }
          placeholder="Little red meat; quick on weekdays"
          value={draft.dietaryNotes}
        />
        <FieldDescription>For the AI when it suggests ideas.</FieldDescription>
      </Field>

      {error ? (
        <p className="text-destructive text-sm" role="alert">
          {error}
        </p>
      ) : null}
      <Button
        className="justify-self-start"
        disabled={pending}
        size="lg"
        type="submit"
      >
        Save preferences
      </Button>
    </form>
  );
}
