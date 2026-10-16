/** Shown for an absent value; screen readers hear "none" instead of a dash. */
export function None() {
  return (
    <span className="muted">
      <span aria-hidden="true">—</span>
      <span className="visually-hidden">none</span>
    </span>
  );
}
