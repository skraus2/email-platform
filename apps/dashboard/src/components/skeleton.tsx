import type { ReactNode } from "react";

/**
 * Loading skeletons: grey placeholders in the shape of the page that is
 * loading, so the page does not jump when it arrives. Each page's loading
 * screen composes them; the placeholders are hidden from assistive
 * technology, which hears the label instead. They appear after a short
 * delay, so a fast load shows nothing at all.
 */
export function Skeleton({
  label,
  children,
}: {
  /** What is loading, announced as a status ("Loading messages…"). */
  label: string;
  children: ReactNode;
}) {
  return (
    <>
      <p role="status" className="visually-hidden">
        {label}
      </p>
      <div className="skeleton" aria-hidden="true">
        {children}
      </div>
    </>
  );
}

type Width = "short" | "medium" | "long" | "full";

function Bar({
  width = "medium",
  className = "",
}: {
  width?: Width;
  className?: string;
}) {
  return <span className={`sk sk-${width} ${className}`.trim()} />;
}

/** The page title, with the intro line under it unless `intro` is false. */
export function SkeletonTitle({ intro = true }: { intro?: boolean }) {
  return (
    <div className="sk-title">
      <Bar width="medium" className="sk-h1" />
      {intro ? <Bar width="long" /> : null}
    </div>
  );
}

/** The link back to the list, above a detail page's title. */
export function SkeletonBack() {
  return <Bar width="short" className="sk-back" />;
}

/** A section heading. */
export function SkeletonHeading() {
  return <Bar width="short" className="sk-h2" />;
}

/** A table card: a header row and `rows` rows of `cols` cells. */
export function SkeletonTable({
  rows,
  cols,
  compact = false,
  caption = false,
}: {
  rows: number;
  /** 2 to 7 columns. */
  cols: number;
  /** As narrow as the short count tables. */
  compact?: boolean;
  /** The table shows its caption (a count line) above the header. */
  caption?: boolean;
}) {
  const cells = (row: number) =>
    Array.from({ length: cols }, (_, c) => (
      <Bar key={c} width={(row + c) % 3 === 0 ? "long" : "medium"} />
    ));
  return (
    <div
      className={`sk-card sk-table sk-cols-${String(cols)}${compact ? " compact" : ""}`}
    >
      {caption ? (
        <div className="sk-row sk-caption">
          <Bar width="short" />
        </div>
      ) : null}
      <div className="sk-row sk-head">{cells(1)}</div>
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="sk-row">
          {cells(r)}
        </div>
      ))}
    </div>
  );
}

/** A detail list card: `rows` label/value rows. */
export function SkeletonCard({ rows }: { rows: number }) {
  return (
    <div className="sk-card sk-meta">
      {Array.from({ length: rows }, (_, r) => (
        <div key={r} className="sk-row">
          <Bar width="short" />
          <Bar width={r % 2 === 0 ? "long" : "medium"} />
        </div>
      ))}
    </div>
  );
}

/** The messages filter panel: fields, status pills and the buttons. */
export function SkeletonFilters() {
  return (
    <div className="sk-filters">
      {/* Search, project, recipient, error code, from, to; most with a
          hint line below. */}
      {[true, false, true, false, true, true].map((hint, i) => (
        <span key={i} className={i === 0 ? "sk-field wide" : "sk-field"}>
          <Bar width="short" />
          <span className="sk sk-control" />
          {hint ? <Bar width="medium" className="sk-hint" /> : null}
        </span>
      ))}
      <span className="sk-field wide">
        <Bar width="short" />
        <span className="sk-pills">
          {Array.from({ length: 9 }, (_, i) => (
            <span key={i} className="sk sk-pill" />
          ))}
        </span>
      </span>
      <span className="sk-field wide sk-actions">
        <span className="sk sk-button" />
        <span className="sk sk-button" />
      </span>
    </div>
  );
}

/** A block of text or a form, e.g. template source or the preview. */
export function SkeletonBlock({
  size,
}: {
  size: "small" | "medium" | "large";
}) {
  return <span className={`sk sk-block sk-block-${size}`} />;
}
