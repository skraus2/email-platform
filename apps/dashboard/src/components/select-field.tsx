"use client";

import {
  useEffect,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type RefObject,
} from "react";
import { Icon } from "@/components/icons";
import {
  useHydrated,
  useOutsideClick,
  usePopupPlacement,
} from "@/components/popup";
import { moveIndex, typeahead } from "@/lib/listbox";

export type SelectOption = {
  value: string;
  label: string;
  /** Shown muted after the label, e.g. "(disabled)". */
  note?: string;
};

/** Typed letters within this time form one type-ahead query. */
const TYPEAHEAD_MS = 500;

/**
 * A select with the dashboard's own popup (WAI-ARIA select-only combobox):
 * a button showing the chosen option and a listbox below it. The value is
 * sent in a hidden input under name. Until the page is interactive it is a
 * native <select> with the same id and name, so the form also works before
 * (or without) JavaScript. The <label for={id}> names it in both forms;
 * label repeats that text for the list.
 */
export function SelectField({
  id,
  name,
  label,
  options,
  defaultValue,
  required,
  "aria-invalid": ariaInvalid,
  "aria-describedby": ariaDescribedBy,
}: {
  id: string;
  name: string;
  /** The field's label text, naming the list. */
  label: string;
  options: SelectOption[];
  defaultValue: string;
  required?: boolean | undefined;
  "aria-invalid"?: boolean | undefined;
  "aria-describedby"?: string | undefined;
}) {
  const hydrated = useHydrated();
  const [value, setValue] = useState(defaultValue);
  // The active (highlighted) option while the list is open, else null.
  const [active, setActive] = useState<number | null>(null);
  const wrapper = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const query = useRef({ text: "", at: 0 });
  const listId = useId();
  const optionId = (i: number) => `${listId}-${String(i)}`;
  const open = active !== null;
  const selected = options.findIndex((o) => o.value === value);
  const current = options[selected];

  useOutsideClick(wrapper, open, () => {
    setActive(null);
  });

  useEffect(() => {
    if (active !== null) {
      document
        .getElementById(optionId(active))
        ?.scrollIntoView({ block: "nearest" });
    }
    // optionId only depends on listId, which never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  if (!hydrated) {
    return (
      <select
        id={id}
        name={name}
        defaultValue={defaultValue}
        required={required}
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.note ? `${o.label} ${o.note}` : o.label}
          </option>
        ))}
      </select>
    );
  }

  const pick = (i: number) => {
    const o = options[i];
    if (o) {
      setValue(o.value);
    }
    setActive(null);
  };

  /** The option type-ahead lands on, keeping the typed letters briefly. */
  const typed = (key: string, from: number) => {
    const now = Date.now();
    const q = query.current;
    q.text = now - q.at < TYPEAHEAD_MS ? q.text + key : key;
    q.at = now;
    return typeahead(
      options.map((o) => o.label),
      q.text,
      from,
    );
  };

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    // Browser shortcuts stay with the browser.
    if (e.ctrlKey || e.metaKey) {
      return;
    }
    const printable = e.key.length === 1 && !e.altKey;
    if (active === null) {
      if (["Enter", " ", "ArrowDown", "ArrowUp"].includes(e.key)) {
        e.preventDefault();
        setActive(Math.max(selected, 0));
      } else if (printable) {
        // Closed, type-ahead changes the value directly, as native selects do.
        e.preventDefault();
        const i = typed(e.key, selected);
        if (i !== undefined) {
          pick(i);
        }
      }
      return;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      setActive(null);
    } else if (e.key === "Enter" || (e.key === "ArrowUp" && e.altKey)) {
      e.preventDefault();
      pick(active);
    } else if (e.key === "Tab") {
      // Picks the active option; the focus moves on as usual.
      pick(active);
    } else if (e.key === " " && Date.now() - query.current.at >= TYPEAHEAD_MS) {
      e.preventDefault();
      pick(active);
    } else if (printable) {
      e.preventDefault();
      const i = typed(e.key, active);
      if (i !== undefined) {
        setActive(i);
      }
    } else {
      const i = moveIndex(e.key, active, options.length);
      if (i !== undefined) {
        e.preventDefault();
        setActive(i);
      }
    }
  };

  return (
    <div className="select-field" ref={wrapper}>
      <input type="hidden" name={name} value={value} />
      <button
        type="button"
        id={id}
        role="combobox"
        className="select-button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={active === null ? undefined : optionId(active)}
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
        onClick={() => {
          setActive(open ? null : Math.max(selected, 0));
        }}
        onKeyDown={onKeyDown}
        onBlur={() => {
          setActive(null);
        }}
      >
        <span className="select-value">
          {current?.label ?? ""}
          {current?.note ? (
            <span className="muted"> {current.note}</span>
          ) : null}
        </span>
        <Icon name="chevronDown" />
      </button>
      {open ? (
        <SelectList
          id={listId}
          label={label}
          options={options}
          selected={selected}
          active={active}
          optionId={optionId}
          list={list}
          anchor={wrapper}
          onActive={setActive}
          onPick={pick}
        />
      ) : null}
    </div>
  );
}

function SelectList({
  id,
  label,
  options,
  selected,
  active,
  optionId,
  list,
  anchor,
  onActive,
  onPick,
}: {
  id: string;
  label: string;
  options: SelectOption[];
  selected: number;
  active: number;
  optionId: (i: number) => string;
  list: RefObject<HTMLUListElement | null>;
  anchor: RefObject<HTMLDivElement | null>;
  onActive: (i: number) => void;
  onPick: (i: number) => void;
}) {
  usePopupPlacement(list, anchor);
  return (
    <ul
      id={id}
      ref={list}
      role="listbox"
      aria-label={label}
      className="popup listbox"
      // The focus stays on the button while the list is used with a mouse.
      onMouseDown={(e) => {
        e.preventDefault();
      }}
    >
      {options.map((o, i) => (
        <li
          key={o.value}
          id={optionId(i)}
          role="option"
          aria-selected={i === selected}
          className={i === active ? "option active" : "option"}
          onMouseMove={() => {
            if (i !== active) {
              onActive(i);
            }
          }}
          onClick={() => {
            onPick(i);
          }}
        >
          <Icon name="check" />
          <span>
            {o.label}
            {o.note ? <span className="muted"> {o.note}</span> : null}
          </span>
        </li>
      ))}
    </ul>
  );
}
