import { act, render, screen } from "@testing-library/react"
import { useLayoutEffect } from "react"
import { afterEach, describe, expect, it } from "vitest"

import { getChatState, updateChatStore } from "@/store/chat"

import { useClawChat } from "./use-claw-chat"

const initialState = getChatState()

afterEach(() => {
  updateChatStore(initialState)
})

function MessageList() {
  const { messages, connectionState } = useClawChat()
  return (
    <div data-testid="chat">
      {connectionState}:{messages.map((m) => m.content).join(",")}
    </div>
  )
}

const message = (id: string, content: string) => ({
  id,
  role: "assistant" as const,
  content,
  timestamp: 0,
})

describe("useClawChat", () => {
  it("re-renders when a message arrives", () => {
    render(<MessageList />)
    expect(screen.getByTestId("chat").textContent).toBe("disconnected:")

    act(() =>
      updateChatStore((prev) => ({
        messages: [...prev.messages, message("1", "hello")],
      })),
    )
    expect(screen.getByTestId("chat").textContent).toBe("disconnected:hello")
  })

  it("shows a change made between its first render and its subscription", () => {
    // A layout effect runs after the list renders but before its passive
    // subscription effect, where jotai 3 dropped its unconditional re-render.
    function ConnectOnMount() {
      useLayoutEffect(() => {
        updateChatStore({
          connectionState: "connected",
          messages: [message("1", "welcome")],
        })
      }, [])
      return null
    }
    render(
      <>
        <MessageList />
        <ConnectOnMount />
      </>,
    )
    expect(screen.getByTestId("chat").textContent).toBe("connected:welcome")
  })
})
