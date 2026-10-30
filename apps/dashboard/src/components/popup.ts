"use client";

import {
  useEffect,
  useLayoutEffect,
  useRef,
  useSyncExternalStore,
  type RefObject,
} from "react";

/**
 * Shared behaviour of the dashboard's popups (calendar, select and
 * suggestion lists): placement below the field and closing on a click
 * outside. Their look is the .popup class.
 */

/** Keeps a popup this far from the viewport's edges. */
const EDGE_GAP = 16;

const noSubscribe = () => () => undefined;

/**
 * False while rendering on the server and hydrating, true afterwards: the
 * fields render their native control until the custom one can work.
 */
export function useHydrated(): boolean {
  return useSyncExternalStore(
    noSubscribe,
    () => true,
    () => false,
  );
}

/**
 * Keeps a popup below its field (positioned by .popup), shifted left when
 * it would leave the viewport, also after a resize.
 */
export function usePopupPlacement(
  popup: RefObject<HTMLElement | null>,
  anchor: RefObject<HTMLElement | null>,
) {
  useLayoutEffect(() => {
    const el = popup.current;
    const field = anchor.current;
    if (!el || !field) {
      return;
    }
    const place = () => {
      el.style.setProperty("left", "0px");
      const box = el.getBoundingClientRect();
      const fieldLeft = field.getBoundingClientRect().left;
      const overflow =
        box.right - (document.documentElement.clientWidth - EDGE_GAP);
      if (overflow > 0) {
        const shift = Math.min(overflow, fieldLeft - EDGE_GAP);
        el.style.setProperty("left", `${String(-Math.max(shift, 0))}px`);
      }
    };
    place();
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("resize", place);
    };
  }, [popup, anchor]);
}

/**
 * Calls onOutside on a click outside wrapper while active, leaving the
 * focus where the click put it. A click, not a press: on touch screens a
 * press also starts every scroll, and scrolling must not close a popup.
 */
export function useOutsideClick(
  wrapper: RefObject<HTMLElement | null>,
  active: boolean,
  onOutside: () => void,
) {
  const callback = useRef(onOutside);
  useLayoutEffect(() => {
    callback.current = onOutside;
  });
  useEffect(() => {
    if (!active) {
      return;
    }
    const onClick = (e: MouseEvent) => {
      if (!wrapper.current?.contains(e.target as Node)) {
        callback.current();
      }
    };
    document.addEventListener("click", onClick);
    return () => {
      document.removeEventListener("click", onClick);
    };
  }, [wrapper, active]);
}
