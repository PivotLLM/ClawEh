import { atom, getDefaultStore } from "jotai"

// Whether the gateway answered this tab's last request. It goes false when a
// same-origin fetch fails at the network level (the gateway is restarting or
// stopped) and true again on the next response of any status. The app shell
// shows a banner while it is false; the chat socket and react-query keep
// retrying on a short cap instead of surfacing the failure.
export const gatewayReachableAtom = atom(true)

const store = getDefaultStore()

export function isGatewayReachable(): boolean {
  return store.get(gatewayReachableAtom)
}

export function setGatewayReachable(reachable: boolean) {
  store.set(gatewayReachableAtom, reachable)
}

/** Run `fn` whenever the gateway comes back after being unreachable. */
export function onGatewayReachable(fn: () => void): () => void {
  let was = isGatewayReachable()
  return store.sub(gatewayReachableAtom, () => {
    const now = isGatewayReachable()
    if (now && !was) fn()
    was = now
  })
}

/** fetch rejects with a TypeError when the server could not be reached at all. */
export function isNetworkError(error: unknown): boolean {
  return error instanceof TypeError
}
