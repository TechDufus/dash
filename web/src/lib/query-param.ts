import { useCallback } from "react";
import { useSearchParams } from "wouter";

/**
 * A string state kept in the URL search params (?key=value), so tabs and filters survive reloads
 * and the command palette can deep-link into them. The fallback value is never written to the URL.
 */
export function useQueryParam(key: string, fallback = ""): [string, (value: string) => void] {
  const [params, setParams] = useSearchParams();
  const value = params.get(key) ?? fallback;
  const set = useCallback(
    (next: string) =>
      setParams(
        (prev) => {
          const n = new URLSearchParams(prev);
          if (next === "" || next === fallback) n.delete(key);
          else n.set(key, next);
          return n;
        },
        { replace: true },
      ),
    [key, fallback, setParams],
  );
  return [value, set];
}
