// xollama: marks a council model in the model picker (docs/xollama/council.mdx).
import { useIsCouncil } from "@/hooks/useCouncil";

export function CouncilBadge({ model }: { model: string | undefined }) {
  const isCouncil = useIsCouncil(model);
  if (!isCouncil) return null;
  return (
    <span
      title="A council: a planner, researchers, critics and a synthesizer answer each turn"
      className="flex-none rounded-full px-1.5 py-px text-[11px] leading-4 font-medium bg-violet-100 text-violet-700 dark:bg-violet-500/20 dark:text-violet-300"
    >
      council
    </span>
  );
}
