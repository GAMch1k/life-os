import { HomeAttentionSurface } from "@/components/home-attention";
import { getHomeAttention } from "@/lib/home-attention";

export const dynamic = "force-dynamic";

export default async function Home() {
  return <HomeAttentionSurface attention={await getHomeAttention()} />;
}
