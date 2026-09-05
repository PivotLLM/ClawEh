import * as React from "react"

const MOBILE_BREAKPOINT = 768
const QUERY = `(max-width: ${MOBILE_BREAKPOINT - 1}px)`

// useSyncExternalStore rather than useState + useEffect: the viewport width is
// an external store, and this is the API React provides for reading one. The
// effect version had to call setIsMobile once on mount to seed the value, which
// meant the first paint always rendered the desktop layout and then corrected
// itself — a visible flash on a phone, and a cascading render every time.
function subscribe(onChange: () => void) {
  const mql = window.matchMedia(QUERY)
  mql.addEventListener("change", onChange)
  return () => mql.removeEventListener("change", onChange)
}

function getSnapshot() {
  return window.innerWidth < MOBILE_BREAKPOINT
}

// The server never renders this app (the SPA is served as static files), but
// useSyncExternalStore requires the third argument for any non-browser render,
// which includes a test renderer without a DOM.
function getServerSnapshot() {
  return false
}

export function useIsMobile() {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)
}
