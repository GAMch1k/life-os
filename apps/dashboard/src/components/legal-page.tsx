/** A plain text page such as the privacy policy, in the app's page style. */
export function LegalPage({
  children,
  title,
  updated,
}: {
  children: React.ReactNode;
  title: string;
  /** When the text last changed, such as "10 October 2026". */
  updated: string;
}) {
  return (
    <article className="mx-auto max-w-3xl space-y-5">
      <header className="space-y-1">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        <p className="text-muted-foreground text-sm">Last updated {updated}</p>
      </header>
      <div className="bg-card space-y-5 rounded-xl border p-4 text-sm leading-relaxed sm:p-6 [&_h2]:text-base [&_h2]:font-semibold [&_section]:space-y-2">
        {children}
      </div>
    </article>
  );
}
