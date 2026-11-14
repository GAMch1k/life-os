import { Sparkles } from "lucide-react";

import { Card, CardContent } from "@/components/ui/card";

/**
 * The frame of the pages outside the app: sign in, the first account, a
 * new password. The app's mark and a title over one card.
 */
export function AccountPage({
  children,
  description,
  title,
}: {
  children: React.ReactNode;
  description?: React.ReactNode;
  title: string;
}) {
  return (
    <div className="mx-auto max-w-sm space-y-4 py-8 sm:py-16">
      <header className="space-y-3 text-center">
        <span className="bg-primary text-primary-foreground mx-auto flex size-10 items-center justify-center rounded-xl">
          <Sparkles aria-hidden="true" className="size-5" />
        </span>
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {description ? (
          <p className="text-muted-foreground text-sm">{description}</p>
        ) : null}
      </header>
      <Card>
        <CardContent className="grid gap-4">{children}</CardContent>
      </Card>
    </div>
  );
}
