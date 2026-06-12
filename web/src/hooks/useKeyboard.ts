import { useEffect } from 'react'

export interface KeyboardActions {
  next: () => void // j / ArrowDown
  prev: () => void // k / ArrowUp
  dispatch: () => void // Enter
  open: () => void // o
  newTicket: () => void // n
  newAgent: () => void // a
  cycleLogMode: () => void // l
  nextTab: () => void // Tab
  prevTab: () => void // Shift+Tab
  selectTab: (index: number) => void // 1-9
  enabled: boolean // false while a dialog owns the keyboard
}

// useKeyboard binds the TUI keymap onto the document so the GUI drives
// identically: j/k navigate, Enter dispatches, o opens, n creates,
// a spawns, l cycles log mode, Tab/1-9 switch projects.
export function useKeyboard(actions: KeyboardActions) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!actions.enabled) return
      const target = e.target as HTMLElement | null
      // Never hijack typing inside form fields.
      if (target && ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)) {
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return

      switch (e.key) {
        case 'j':
        case 'ArrowDown':
          e.preventDefault()
          actions.next()
          break
        case 'k':
        case 'ArrowUp':
          e.preventDefault()
          actions.prev()
          break
        case 'Enter':
          actions.dispatch()
          break
        case 'o':
          actions.open()
          break
        case 'n':
          actions.newTicket()
          break
        case 'a':
          actions.newAgent()
          break
        case 'l':
          actions.cycleLogMode()
          break
        case 'Tab':
          e.preventDefault()
          if (e.shiftKey) {
            actions.prevTab()
          } else {
            actions.nextTab()
          }
          break
        default:
          if (e.key >= '1' && e.key <= '9') {
            actions.selectTab(Number(e.key) - 1)
          }
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [actions])
}
