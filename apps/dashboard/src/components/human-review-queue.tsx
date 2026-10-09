"use client";

import { useState, useTransition } from "react";
import { AlertTriangle, Check } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { inboxClient } from "@/lib/api/inbox-client";
import type { HumanReviewItem } from "@/lib/human-review";

type Candidate = { id?: string; title?: string; notes?: string };
type Review = { errorSummary?: string; candidates?: Candidate[] };

export function HumanReviewQueue({
  initialItems,
}: {
  initialItems: HumanReviewItem[];
}) {
  const [items, setItems] = useState(initialItems);
  const [error, setError] = useState<string>();
  const [pending, startTransition] = useTransition();
  function reviewFor(item: HumanReviewItem): Review {
    return (item.processingMetadata?.humanReview as Review | undefined) ?? {};
  }
  function choose(itemId: string, candidateId: string) {
    setError(undefined);
    startTransition(async () => {
      const { data, error: requestError } = await inboxClient.POST(
        "/v1/inbox-items/{itemId}/human-review/resolve",
        { params: { path: { itemId } }, body: { candidateId } },
      );
      if (requestError || !data) {
        setError("Could not submit that review decision. Please try again.");
        return;
      }
      setItems((current) => current.filter((item) => item.id !== itemId));
    });
  }
  if (!items.length) return null;
  return (
    <Card id="human-review">
      <CardHeader>
        <div className="flex items-center gap-2">
          <AlertTriangle className="size-4 text-amber-600" />
          <CardTitle>Human Review</CardTitle>
        </div>
        <CardDescription>
          {items.length} capture{items.length === 1 ? " needs" : "s need"} your
          decision before Life OS changes anything.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {items.map((item) => {
          const review = reviewFor(item);
          return (
            <section className="rounded-lg border p-4" key={item.id}>
              <p className="font-medium">{item.content}</p>
              <p className="text-muted-foreground mt-1 text-sm">
                {review.errorSummary ?? "Choose the intended Task."}
              </p>
              <div className="mt-3 flex flex-wrap gap-2">
                {review.candidates?.map((candidate) =>
                  candidate.id ? (
                    <Button
                      disabled={pending}
                      key={candidate.id}
                      onClick={() => choose(item.id, candidate.id!)}
                      size="sm"
                      variant="outline"
                    >
                      <Check /> {candidate.title ?? candidate.id}
                    </Button>
                  ) : null,
                )}
              </div>
            </section>
          );
        })}
        {error ? <p className="text-destructive text-sm">{error}</p> : null}
      </CardContent>
    </Card>
  );
}
