export function Footer({
  connected,
  logMode,
  flash,
  showTabs,
}: {
  connected: boolean
  logMode: string
  flash: string
  showTabs: boolean
}) {
  return (
    <div className="footer">
      <span className={connected ? 'live' : 'disconnected'}>
        {connected ? '↻ live' : '⊘ disconnected'}
      </span>
      {logMode && <span>log:{logMode}</span>}
      {flash && <span className="flash">{flash}</span>}
      <span className="hints">
        {showTabs && (
          <>
            <kbd>Tab</kbd> switch{' '}
          </>
        )}
        <kbd>j</kbd>/<kbd>k</kbd> nav <kbd>⏎</kbd> dispatch <kbd>o</kbd> open{' '}
        <kbd>n</kbd> new <kbd>a</kbd> agent <kbd>l</kbd> log
      </span>
    </div>
  )
}
