// xollama: is a model a council (docs/xollama/council.mdx)? /api/show carries
// the model's own settings in its `xollama` block, which upstream's
// capabilities list does not, so the app reads it here.
import { useQuery } from "@tanstack/react-query";
import { ollamaClient as ollama } from "@/lib/ollama-client";

// isCouncilShow reads a /api/show answer. The ollama client's ShowResponse
// type does not know the fork's block, so it is read without it.
export function isCouncilShow(show: unknown): boolean {
  const x = (show as { xollama?: { council?: { enabled?: unknown } } } | null)
    ?.xollama;
  return x?.council?.enabled === true;
}

export function useIsCouncil(modelName: string | undefined): boolean {
  const { data } = useQuery<boolean, Error>({
    queryKey: ["xollamaCouncil", modelName],
    queryFn: async () => {
      try {
        return isCouncilShow(await ollama.show({ model: modelName! }));
      } catch {
        return false; // not pulled yet, or not reachable: an ordinary model
      }
    },
    enabled: !!modelName,
    // Short enough that `xollama tweak model --council` shows up soon.
    staleTime: 60 * 1000,
    gcTime: 60 * 60 * 1000,
  });
  return data ?? false;
}

// The deliberation toggle is the viewer's own preference, kept in this
// browser only. It defaults to on, as the council's show_deliberation does;
// upstream's ThinkEnabled setting defaults to off and is shared with other
// models' think buttons, so it is not reused.
const deliberationKey = "xollama.council.deliberation";

export function readDeliberation(storage?: Pick<Storage, "getItem">): boolean {
  try {
    return (storage ?? window.localStorage).getItem(deliberationKey) !== "off";
  } catch {
    return true;
  }
}

export function writeDeliberation(
  on: boolean,
  storage?: Pick<Storage, "setItem">,
): void {
  try {
    (storage ?? window.localStorage).setItem(
      deliberationKey,
      on ? "on" : "off",
    );
  } catch {
    // Storage blocked: the toggle still works for this page.
  }
}
