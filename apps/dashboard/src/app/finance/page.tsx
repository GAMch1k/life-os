import { FinanceSummary } from "@/components/finance-summary";
import { getFinanceSummary } from "@/lib/finance-summary";
export const dynamic = "force-dynamic";
export default async function FinancePage() {
  return <FinanceSummary state={await getFinanceSummary()} />;
}
