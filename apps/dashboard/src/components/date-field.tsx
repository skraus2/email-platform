"use client";

import {
  createContext,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
} from "react";
import { Icon } from "@/components/icons";
import {
  useHydrated,
  useOutsideClick,
  usePopupPlacement,
} from "@/components/popup";
import {
  MAX_DAY,
  MIN_DAY,
  WEEKDAYS,
  addMonths,
  dayLabel,
  formatDay,
  inRange,
  keyTarget,
  monthGrid,
  monthLabel,
  parseDay,
  sameMonth,
  todayUTC,
  type Day,
} from "@/lib/calendar";

type RangeEnd = "from" | "to";

const RangeContext = createContext<{
  values: Record<RangeEnd, string>;
  set: (end: RangeEnd, value: string) => void;
} | null>(null);

/**
 * Shares the values of a from and a to date field, so each calendar shows
 * the range between them. Renders no element of its own.
 */
export function DateRange({
  from,
  to,
  children,
}: {
  from: string;
  to: string;
  children: ReactNode;
}) {
  const [values, setValues] = useState({ from, to });
  return (
    <RangeContext
      value={{
        values,
        set: (end, value) => {
          setValues((prev) => ({ ...prev, [end]: value }));
        },
      }}
    >
      {children}
    </RangeContext>
  );
}

/**
 * A YYYY-MM-DD text field with a calendar popup. The text field is the form
 * value, so the field also works before (or without) JavaScript; the button
 * that opens the calendar appears once the page is interactive. All days are
 * UTC days.
 */
export function DateField({
  id,
  name,
  defaultValue,
  label,
  range,
  disableFuture = false,
  "aria-invalid": ariaInvalid,
  "aria-describedby": ariaDescribedBy,
}: {
  id: string;
  name: string;
  defaultValue: string;
  /** What is chosen, for the button and dialog names ("from date"). */
  label: string;
  /** Which end of the surrounding DateRange this field is. */
  range?: RangeEnd;
  /** Days after today (UTC) cannot be picked; typing them is still possible. */
  disableFuture?: boolean;
  "aria-invalid"?: boolean | undefined;
  "aria-describedby"?: string | undefined;
}) {
  const shared = useContext(RangeContext);
  const [local, setLocal] = useState(defaultValue);
  const inShared = shared !== null && range !== undefined;
  const value = inShared ? shared.values[range] : local;
  const setValue = (v: string) => {
    if (inShared) {
      shared.set(range, v);
    } else {
      setLocal(v);
    }
  };
  // Today is fixed when the calendar opens, so it does not change under it.
  const [open, setOpen] = useState<{ today: Day; focus: Day } | null>(null);
  const hydrated = useHydrated();
  const wrapper = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const dialogId = useId();
  const dialogLabel = `Choose ${label}`;

  const close = (restoreFocus: boolean) => {
    setOpen(null);
    if (restoreFocus) {
      button.current?.focus();
    }
  };

  useOutsideClick(wrapper, open !== null, () => {
    setOpen(null);
  });

  return (
    <div className="date-field" ref={wrapper}>
      <input
        type="text"
        id={id}
        name={name}
        value={value}
        onChange={(e) => {
          setValue(e.target.value);
        }}
        // Typing closes the calendar: focus must not sit outside an open
        // dialog.
        onFocus={() => {
          setOpen(null);
        }}
        autoComplete="off"
        spellCheck={false}
        maxLength={10}
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
      />
      {hydrated ? (
        <button
          type="button"
          ref={button}
          className="date-field-button"
          aria-label={dialogLabel}
          aria-haspopup="dialog"
          aria-expanded={open !== null}
          aria-controls={open ? dialogId : undefined}
          onClick={() => {
            if (open) {
              close(true);
              return;
            }
            const today = todayUTC(new Date());
            setOpen({ today, focus: parseDay(value) ?? today });
          }}
        >
          <Icon name="calendar" />
        </button>
      ) : null}
      {open ? (
        <CalendarDialog
          id={dialogId}
          label={dialogLabel}
          selected={parseDay(value)}
          rangeFrom={inShared ? parseDay(shared.values.from) : undefined}
          rangeTo={inShared ? parseDay(shared.values.to) : undefined}
          today={open.today}
          max={disableFuture ? open.today : MAX_DAY}
          initialFocus={open.focus}
          anchor={wrapper}
          onSelect={(d) => {
            setValue(d === null ? "" : formatDay(d));
            close(true);
          }}
          onClose={() => {
            close(true);
          }}
        />
      ) : null}
    </div>
  );
}

/**
 * The calendar popup (WAI-ARIA date picker dialog): month navigation, a grid
 * of days with one focusable day, Today and Clear. Focus stays inside until
 * a day is chosen or Escape closes it.
 */
function CalendarDialog({
  id,
  label,
  selected,
  rangeFrom,
  rangeTo,
  today,
  max,
  initialFocus,
  anchor,
  onSelect,
  onClose,
}: {
  id: string;
  label: string;
  selected: Day | undefined;
  /** Ends of the range this field belongs to, if any. */
  rangeFrom: Day | undefined;
  rangeTo: Day | undefined;
  today: Day;
  /** Last selectable day. */
  max: Day;
  initialFocus: Day;
  anchor: RefObject<HTMLElement | null>;
  /** A chosen day, or null for Clear. */
  onSelect: (d: Day | null) => void;
  onClose: () => void;
}) {
  const [focus, setFocus] = useState(initialFocus);
  // Set when the focused day should also get the DOM focus: on open and on
  // grid keys, not when the month buttons change the month.
  const moveFocus = useRef(true);
  const dialog = useRef<HTMLDivElement>(null);
  const grid = useRef<HTMLTableElement>(null);
  const headingId = useId();

  useEffect(() => {
    if (moveFocus.current) {
      moveFocus.current = false;
      grid.current
        ?.querySelector<HTMLButtonElement>('button[tabindex="0"]')
        ?.focus();
    }
  }, [focus]);

  usePopupPlacement(dialog, anchor);

  const days = monthGrid(focus);
  const weeks = Array.from({ length: 6 }, (_, i) =>
    days.slice(i * 7, i * 7 + 7),
  );
  const atFirstMonth = sameMonth(focus, MIN_DAY);
  const atLastMonth = sameMonth(focus, max) || focus > max;

  const onGridKeyDown = (e: KeyboardEvent) => {
    // Browser shortcuts such as Alt+ArrowLeft stay with the browser.
    if (e.altKey || e.ctrlKey || e.metaKey) {
      return;
    }
    const next = keyTarget(e.key, e.shiftKey, focus);
    if (next === undefined) {
      return;
    }
    e.preventDefault();
    moveFocus.current = true;
    setFocus(next);
  };

  const onDialogKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab") {
      return;
    }
    const items = Array.from(
      e.currentTarget.querySelectorAll<HTMLElement>("button"),
    ).filter((b) => b.tabIndex >= 0);
    const first = items[0];
    const last = items.at(-1);
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last?.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first?.focus();
    }
  };

  return (
    <div
      id={id}
      ref={dialog}
      role="dialog"
      aria-modal="true"
      aria-label={label}
      className="popup calendar"
      onKeyDown={onDialogKeyDown}
    >
      <div className="calendar-head">
        <button
          type="button"
          className="calendar-nav"
          aria-label="Previous month"
          aria-disabled={atFirstMonth || undefined}
          onClick={(e) => {
            // Safari does not focus a clicked button; the focused day may
            // leave the grid, so keep the focus inside the dialog.
            e.currentTarget.focus();
            if (!atFirstMonth) {
              setFocus(addMonths(focus, -1));
            }
          }}
        >
          <Icon name="chevronLeft" />
        </button>
        <div id={headingId} className="calendar-month" aria-live="polite">
          {monthLabel(focus)}
        </div>
        <button
          type="button"
          className="calendar-nav"
          aria-label="Next month"
          aria-disabled={atLastMonth || undefined}
          onClick={(e) => {
            // Safari does not focus a clicked button; the focused day may
            // leave the grid, so keep the focus inside the dialog.
            e.currentTarget.focus();
            if (!atLastMonth) {
              setFocus(addMonths(focus, 1));
            }
          }}
        >
          <Icon name="chevronRight" />
        </button>
      </div>
      <table
        ref={grid}
        role="grid"
        aria-labelledby={headingId}
        className="calendar-grid"
        onKeyDown={onGridKeyDown}
      >
        <thead>
          <tr>
            {WEEKDAYS.map((w) => (
              <th key={w.short} scope="col" abbr={w.long}>
                {w.short}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {weeks.map((week) => (
            <tr key={week[0]}>
              {week.map((d) => {
                const disabled = d < MIN_DAY || d > max;
                const start = d === rangeFrom;
                const end = d === rangeTo;
                const ends = [start ? "from" : "", end ? "to" : ""]
                  .filter(Boolean)
                  .join(" and ");
                return (
                  <td
                    key={d}
                    aria-selected={d === selected || undefined}
                    data-range-start={start ? "" : undefined}
                    data-range-end={end ? "" : undefined}
                    data-in-range={
                      inRange(d, rangeFrom, rangeTo) ? "" : undefined
                    }
                  >
                    <button
                      type="button"
                      className="calendar-day"
                      tabIndex={d === focus ? 0 : -1}
                      aria-label={
                        ends ? `${dayLabel(d)}, ${ends} date` : dayLabel(d)
                      }
                      aria-current={d === today ? "date" : undefined}
                      aria-disabled={disabled || undefined}
                      data-outside={sameMonth(d, focus) ? undefined : ""}
                      onClick={(e) => {
                        if (disabled) {
                          // Focus it (Safari does not on click) and keep
                          // the grid in step.
                          e.currentTarget.focus();
                          setFocus(d);
                        } else {
                          onSelect(d);
                        }
                      }}
                    >
                      {new Date(d).getUTCDate()}
                    </button>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="calendar-foot">
        <button
          type="button"
          onClick={() => {
            onSelect(today);
          }}
        >
          Today
        </button>
        <button
          type="button"
          onClick={() => {
            onSelect(null);
          }}
        >
          Clear
        </button>
      </div>
    </div>
  );
}
