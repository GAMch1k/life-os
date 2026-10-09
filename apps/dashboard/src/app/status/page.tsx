import { RuntimeStatusView } from "@/components/runtime-status";
import { getRuntimeStatus } from "@/lib/runtime-status";

export const dynamic = "force-dynamic";

export default async function StatusPage() {
  return <RuntimeStatusView status={await getRuntimeStatus()} />;
}
