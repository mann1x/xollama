// xollama: shows or hides a council's deliberation (docs/xollama/council.mdx).
// Off sends think:false, which a council answers with the answer alone.
import { forwardRef } from "react";

interface DeliberationButtonProps {
  isActive: boolean;
  onToggle: () => void;
}

export const DeliberationButton = forwardRef<
  HTMLButtonElement,
  DeliberationButtonProps
>(function DeliberationButton({ isActive, onToggle }, ref) {
  return (
    <button
      ref={ref}
      type="button"
      aria-pressed={isActive}
      title={
        isActive
          ? "Hide the council's deliberation: get the answer alone"
          : "Show the council's deliberation: the plan, findings and critiques"
      }
      onClick={onToggle}
      className={`select-none flex items-center justify-center gap-1.5 rounded-full h-9 px-3 bg-white dark:bg-neutral-700 focus:outline-none focus:ring-2 focus:ring-blue-500 cursor-pointer transition-all whitespace-nowrap border border-transparent text-sm ${
        isActive
          ? "text-[rgba(0,115,255,1)] dark:text-[rgba(70,155,255,1)]"
          : "text-neutral-500 dark:text-neutral-400"
      }`}
    >
      <svg
        className="w-3.5 h-3.5 flex-none"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        <circle cx="12" cy="6" r="2.5" />
        <circle cx="5" cy="17" r="2.5" />
        <circle cx="19" cy="17" r="2.5" />
        <path d="M10.5 8.2 6.5 14.8M13.5 8.2l4 6.6M7.5 17h9" />
      </svg>
      Deliberation
    </button>
  );
});
