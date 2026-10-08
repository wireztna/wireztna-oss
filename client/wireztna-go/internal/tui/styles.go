package tui

import "github.com/charmbracelet/lipgloss"

// ─── Color Palette (k9s-inspired dark theme) ───

var (
	// Base colors
	cPrimary   = lipgloss.Color("#6C63FF") // purple accent (brand)
	cSecondary = lipgloss.Color("#40E0D0") // teal/cyan for highlights
	cSuccess   = lipgloss.Color("#00E676") // green
	cDanger    = lipgloss.Color("#FF5252") // red
	cWarning   = lipgloss.Color("#FFD740") // amber
	cInfo      = lipgloss.Color("#448AFF") // blue

	// Neutrals
	cFg       = lipgloss.Color("#E0E0E0") // main text
	cFgDim    = lipgloss.Color("#757575") // secondary text
	cFgMuted  = lipgloss.Color("#4A4A4A") // borders, very subtle
	cBorder   = lipgloss.Color("#3A3A5C") // border color
	cBorderHi = lipgloss.Color("#6C63FF") // highlighted border
)

// ─── Layout Styles ───

var (
	// Header bar (top row with logo + version)
	sHeader = lipgloss.NewStyle().
		Bold(true).
		Foreground(cSecondary)

	sVersion = lipgloss.NewStyle().
			Foreground(cFgDim).
			Italic(true)

	// Tab bar
	sTabActive = lipgloss.NewStyle().
			Bold(true).
			Foreground(cPrimary).
			Background(lipgloss.Color("#2A2A4A")).
			Padding(0, 2)

	sTabInactive = lipgloss.NewStyle().
			Foreground(cFgDim).
			Padding(0, 2)

	// Status crumb bar (compact one-line info)
	sCrumbBar = lipgloss.NewStyle().
			Foreground(cFg)

	sCrumbSep = lipgloss.NewStyle().
			Foreground(cFgMuted)

	sCrumbVal = lipgloss.NewStyle().
			Foreground(cSecondary).
			Bold(true)

	// Section title (above tables)
	sPanelTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cSecondary)

	// Table styles
	sTableHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(cSecondary).
			Padding(0, 1)

	sTableCell = lipgloss.NewStyle().
			Foreground(cFg).
			Padding(0, 1)

	sTableCellDim = lipgloss.NewStyle().
			Foreground(cFgDim).
			Padding(0, 1)

	// Status indicators
	sStatusOn = lipgloss.NewStyle().
			Bold(true).
			Foreground(cSuccess)

	sStatusOff = lipgloss.NewStyle().
			Bold(true).
			Foreground(cDanger)

	sStatusWarn = lipgloss.NewStyle().
			Bold(true).
			Foreground(cWarning)

	sStatusInfo = lipgloss.NewStyle().
			Bold(true).
			Foreground(cInfo)

	// Help bar (bottom footer)
	sHelpKey = lipgloss.NewStyle().
			Bold(true).
			Foreground(cPrimary)

	sHelpDesc = lipgloss.NewStyle().
			Foreground(cFgDim)

	// Field key:value rows
	sFieldKey = lipgloss.NewStyle().
			Foreground(cFgDim).
			Width(16).
			Align(lipgloss.Right)

	sFieldVal = lipgloss.NewStyle().
			Foreground(cFg).
			PaddingLeft(1)

	// Selector / list items
	sSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(cPrimary)

	sListItemDim = lipgloss.NewStyle().
			Foreground(cFgDim).
			Strikethrough(true)

	// Error banner
	sError = lipgloss.NewStyle().
		Foreground(cDanger).
		Bold(true)

	// Crumb / breadcrumb
	sCrumb = lipgloss.NewStyle().
		Foreground(cFgDim).
		Italic(true)

	// Sparkline colors
	sSparkLow  = lipgloss.NewStyle().Foreground(cFgDim)
	sSparkMed  = lipgloss.NewStyle().Foreground(cInfo)
	sSparkHigh = lipgloss.NewStyle().Foreground(cSuccess)
)
