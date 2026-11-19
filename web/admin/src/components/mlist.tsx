import type { ReactNode } from "react";

// Lists for phones, in the style of iOS Settings: a leading icon, a title
// and subtitle, a trailing value and a chevron. Pages render these instead
// of their tables on narrow screens.

export function MList({ children }: { children: ReactNode }) {
  return <div className="mlist phone-only">{children}</div>;
}

export function MRow({
  lead,
  title,
  sub,
  trail,
  onClick,
  muted,
  selected,
}: {
  lead?: ReactNode;
  title: ReactNode;
  sub?: ReactNode;
  trail?: ReactNode;
  onClick?: () => void;
  muted?: boolean;
  selected?: boolean;
}) {
  return (
    <button type="button" className={`mrow ${muted ? "is-muted" : ""} ${selected ? "is-selected" : ""}`} onClick={onClick}>
      {lead && <span className="mrow-lead">{lead}</span>}
      <span className="mrow-main">
        <span className="mrow-title">{title}</span>
        {sub && <span className="mrow-sub">{sub}</span>}
      </span>
      {trail && <span className="mrow-trail">{trail}</span>}
      {onClick && (
        <svg className="mrow-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.2} strokeLinecap="round" aria-hidden>
          <path d="M9 6l6 6-6 6" />
        </svg>
      )}
    </button>
  );
}

// IconTile is a rounded coloured square with an icon, as in iOS Settings.
export function IconTile({ tone, children }: { tone: "blue" | "green" | "violet" | "orange" | "gray" | "teal"; children: ReactNode }) {
  return <span className={`nav-tile tile-${tone} mrow-tile`}>{children}</span>;
}
