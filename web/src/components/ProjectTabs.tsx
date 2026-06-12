export interface Tab {
  name: string
  dir: string // empty for the synthetic "Other" tab
}

export function ProjectTabs({
  tabs,
  active,
  onSelect,
}: {
  tabs: Tab[]
  active: number
  onSelect: (index: number) => void
}) {
  // Matches the TUI: the tab bar only appears with two or more tabs.
  if (tabs.length < 2) return null
  return (
    <div className="tabs">
      {tabs.map((tab, i) => (
        <button
          key={tab.name + tab.dir}
          className={i === active ? 'active' : ''}
          onClick={() => onSelect(i)}
        >
          {i + 1}:{tab.name}
        </button>
      ))}
    </div>
  )
}
