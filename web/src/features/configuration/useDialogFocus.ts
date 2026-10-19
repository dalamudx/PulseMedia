import { useEffect, useRef, type RefObject } from "react";
// Scoped to an existing dialog: own listeners, trap focus, restore the trigger.
export function useDialogFocus(ref: RefObject<HTMLElement | null>, onClose: () => void, active = true, returnFocus?: RefObject<HTMLElement | null>) {
 const close = useRef(onClose); close.current = onClose;
 useEffect(() => {
  const dialog = ref.current; if (!active || !dialog) return;
  const trigger = returnFocus?.current ?? (document.activeElement instanceof HTMLElement ? document.activeElement : null);
  const controls = () => Array.from(dialog.querySelectorAll<HTMLElement>('button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),a[href],[tabindex="0"]')).filter(item => !item.matches(':disabled') && item.getClientRects().length > 0);
  (controls()[0] ?? dialog).focus();
  function keydown(event: KeyboardEvent) {
   if (event.key === 'Escape') { event.preventDefault(); close.current(); return; }
   if (event.key !== 'Tab') return;
   const options = controls(); if (!options.length) { event.preventDefault(); dialog?.focus(); return; }
   const first = options[0], last = options[options.length - 1];
   if (event.shiftKey && (document.activeElement === first || !dialog?.contains(document.activeElement))) { event.preventDefault(); last.focus(); }
   else if (!event.shiftKey && (document.activeElement === last || !dialog?.contains(document.activeElement))) { event.preventDefault(); first.focus(); }
  }
  document.addEventListener('keydown', keydown);
  return () => { document.removeEventListener('keydown', keydown); if (trigger?.isConnected) trigger.focus(); };
 }, [ref, active, returnFocus]);
}
