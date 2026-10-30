"use client";

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Icon } from "@/components/icons";
import {
  nativeControl,
  useHydrated,
  useOutsideClick,
} from "@/components/popup";
import { OptionList } from "@/components/select-field";
import { filterOptions, moveIndex } from "@/lib/listbox";

/**
 * A text field with suggestions in the dashboard's own popup (WAI-ARIA
 * editable combobox with list autocomplete): typing filters the
 * suggestions, any text is allowed. The arrow button shows all of them.
 * Until the page is interactive the same text field uses a native
 * <datalist>, so the form also works before (or without) JavaScript, and
 * what was typed there stays.
 */
export function ComboboxField({
  id,
  name,
  label,
  showLabel,
  suggestions,
  defaultValue,
  maxLength,
  "aria-invalid": ariaInvalid,
  "aria-describedby": ariaDescribedBy,
}: {
  id: string;
  name: string;
  /** The field's label text, naming the list. */
  label: string;
  /** Name of the arrow button, e.g. "Show error codes". */
  showLabel: string;
  suggestions: readonly string[];
  defaultValue: string;
  maxLength?: number;
  "aria-invalid"?: boolean | undefined;
  "aria-describedby"?: string | undefined;
}) {
  const hydrated = useHydrated();
  const [value, setValue] = useState(
    () => nativeControl(id, hydrated)?.value ?? defaultValue,
  );
  // Closed, open with the matches of the text, or open with all suggestions
  // (arrow button).
  const [open, setOpen] = useState<"closed" | "matches" | "all">("closed");
  const [active, setActive] = useState(-1);
  const wrapper = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  // Set when the pointer moved the highlight: the list must not scroll then.
  const byPointer = useRef(false);
  const listId = useId();
  const optionId = (i: number) => `${listId}-${String(i)}`;
  const shown =
    open === "all" ? [...suggestions] : filterOptions(suggestions, value);
  const expanded = hydrated && open !== "closed" && shown.length > 0;
  const activeId = expanded && active >= 0 ? optionId(active) : undefined;

  const close = () => {
    setOpen("closed");
    setActive(-1);
  };

  useOutsideClick(wrapper, expanded, close);

  useEffect(() => {
    if (byPointer.current) {
      byPointer.current = false;
    } else if (activeId !== undefined) {
      document.getElementById(activeId)?.scrollIntoView({ block: "nearest" });
    }
  }, [activeId]);

  const pick = (i: number) => {
    const s = shown[i];
    if (s !== undefined) {
      setValue(s);
    }
    close();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.ctrlKey || e.metaKey) {
      return;
    }
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp": {
        e.preventDefault();
        if (!expanded) {
          setOpen("matches");
          // Alt+ArrowDown only opens; the arrows also enter the list.
          setActive(
            e.altKey
              ? -1
              : e.key === "ArrowDown"
                ? 0
                : filterOptions(suggestions, value).length - 1,
          );
          return;
        }
        setActive(moveIndex(e.key, active, shown.length) ?? -1);
        return;
      }
      case "Enter":
        // Without an active suggestion, Enter submits the form as usual.
        if (expanded && active >= 0) {
          e.preventDefault();
          pick(active);
        }
        return;
      case "Escape":
        if (expanded) {
          e.preventDefault();
          e.stopPropagation();
          close();
        }
        return;
      case "Home":
      case "End":
      case "ArrowLeft":
      case "ArrowRight":
        // These move the caret; the list loses its highlight.
        setActive(-1);
        return;
      default:
        return;
    }
  };

  return (
    <div className="combobox-field" ref={wrapper}>
      {/* The same element before and after hydration, so typed text and the
          focus survive it; only the native list and the ARIA change. */}
      <input
        ref={input}
        type="text"
        id={id}
        name={name}
        list={hydrated ? undefined : listId}
        role={hydrated ? "combobox" : undefined}
        value={value}
        onChange={(e) => {
          setValue(e.target.value);
          setOpen("matches");
          setActive(-1);
        }}
        onKeyDown={onKeyDown}
        onBlur={close}
        autoComplete="off"
        autoCapitalize="none"
        spellCheck={false}
        maxLength={maxLength}
        aria-autocomplete={hydrated ? "list" : undefined}
        aria-expanded={hydrated ? expanded : undefined}
        aria-controls={expanded ? listId : undefined}
        aria-activedescendant={activeId}
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
      />
      {hydrated ? (
        <button
          type="button"
          tabIndex={-1}
          className="combobox-button"
          aria-label={showLabel}
          aria-expanded={expanded}
          aria-controls={expanded ? listId : undefined}
          // The focus stays in the text field.
          onMouseDown={(e) => {
            e.preventDefault();
          }}
          onClick={() => {
            input.current?.focus();
            if (expanded) {
              close();
            } else {
              setOpen("all");
              setActive(-1);
            }
          }}
        >
          <Icon name="chevronDown" />
        </button>
      ) : (
        <datalist id={listId}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      )}
      {expanded ? (
        <OptionList
          id={listId}
          label={label}
          options={shown.map((s) => ({ value: s, label: s }))}
          selected={shown.indexOf(value)}
          active={active}
          optionId={optionId}
          list={list}
          anchor={wrapper}
          onActive={(i) => {
            byPointer.current = true;
            setActive(i);
          }}
          onPick={pick}
        />
      ) : null}
    </div>
  );
}
