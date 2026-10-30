"use client";

import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { Icon } from "@/components/icons";
import { useHydrated, useOutsideClick } from "@/components/popup";
import { OptionList } from "@/components/select-field";
import { filterOptions, moveIndex } from "@/lib/listbox";

/**
 * A text field with suggestions in the dashboard's own popup (WAI-ARIA
 * editable combobox with list autocomplete): typing filters the
 * suggestions, any text is allowed. The arrow button shows all of them.
 * Until the page is interactive it is a native <input list> with a
 * <datalist>, so the form also works before (or without) JavaScript.
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
  const [value, setValue] = useState(defaultValue);
  // Closed, open with the matches of the text, or open with all suggestions
  // (arrow button).
  const [open, setOpen] = useState<"closed" | "matches" | "all">("closed");
  const [active, setActive] = useState(-1);
  const wrapper = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const listId = useId();
  const optionId = (i: number) => `${listId}-${String(i)}`;
  const shown =
    open === "all" ? [...suggestions] : filterOptions(suggestions, value);
  const expanded = open !== "closed" && shown.length > 0;

  const close = () => {
    setOpen("closed");
    setActive(-1);
  };

  useOutsideClick(wrapper, expanded, close);

  useEffect(() => {
    if (active >= 0) {
      document
        .getElementById(optionId(active))
        ?.scrollIntoView({ block: "nearest" });
    }
    // optionId only depends on listId, which never changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  if (!hydrated) {
    return (
      <>
        <input
          type="text"
          id={id}
          name={name}
          list={listId}
          defaultValue={defaultValue}
          autoComplete="off"
          spellCheck={false}
          maxLength={maxLength}
          aria-invalid={ariaInvalid}
          aria-describedby={ariaDescribedBy}
        />
        <datalist id={listId}>
          {suggestions.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
      </>
    );
  }

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
        // These move the caret; the list loses its highlight.
        setActive(-1);
        return;
      default:
        return;
    }
  };

  return (
    <div className="combobox-field" ref={wrapper}>
      <input
        ref={input}
        type="text"
        id={id}
        name={name}
        role="combobox"
        value={value}
        onChange={(e) => {
          setValue(e.target.value);
          setOpen("matches");
          setActive(-1);
        }}
        onKeyDown={onKeyDown}
        onBlur={close}
        autoComplete="off"
        spellCheck={false}
        maxLength={maxLength}
        aria-autocomplete="list"
        aria-expanded={expanded}
        aria-controls={expanded ? listId : undefined}
        aria-activedescendant={
          expanded && active >= 0 ? optionId(active) : undefined
        }
        aria-invalid={ariaInvalid}
        aria-describedby={ariaDescribedBy}
      />
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
          onActive={setActive}
          onPick={pick}
        />
      ) : null}
    </div>
  );
}
