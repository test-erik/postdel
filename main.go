package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
)

// mailqIDsMsg holds the list of queue IDs parsed from mailq.
type mailqIDsMsg []string

// postcatMsg is the output of "postcat -q <ID>".
type postcatMsg string

// errorMsg represents any error running external commands.
type errorMsg error

// model represents the entire TUI state.
type model struct {
	showWarning  bool
	warningReady bool
	warningView  viewport.Model

	entries  []string // all Queue-IDs from mailq
	selected int
	ready    bool

	left      viewport.Model
	right     viewport.Model
	leftRaw   string // raw text for left
	rightRaw  string // raw text for right
	err       error
	focus     int // 0=left, 1=right

	showDeleteDialog bool
	termWidth        int
	termHeight       int

	// Flag, ob wir gerade frisch gelöscht haben
	justDeleted bool
}

// Styling
var (
	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229"))

	warningBorder = lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			Padding(1, 2).
			Foreground(lipgloss.Color("196"))

	focusBorderColor = lipgloss.Color("229")

	dialogBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Width(30)
)

// Run mailq, parse IDs.
func runMailqCmd() tea.Msg {
	out, err := exec.Command("mailq").Output()
	if err != nil {
		return errorMsg(err)
	}
	ids := parseMailqForIDs(out)
	return mailqIDsMsg(ids)
}

// Run postcat -q <ID>.
func runPostcatCmd(queueID string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command("/usr/sbin/postcat", "-q", queueID).Output()
		if err != nil {
			return errorMsg(err)
		}
		return postcatMsg(out)
	}
}

// parseMailqForIDs scans mailq output for something that looks like a queue ID.
func parseMailqForIDs(output []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var ids []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 && looksLikeQueueID(fields[0]) {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// Simplistic check for a Postfix-like queue ID.
func looksLikeQueueID(s string) bool {
	if len(s) < 3 || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') &&
			(r < 'A' || r > 'Z') &&
			(r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// Der eigentliche Löschbefehl. Anschließend refresh per mailq.
func (m *model) deleteQueueID() tea.Cmd {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return nil
	}
	id := m.entries[m.selected]

	out, err := exec.Command("postsuper", "-d", id).CombinedOutput()
	if err != nil {
		m.err = fmt.Errorf("error running postsuper -d %s: %w\nOutput:\n%s", id, err, string(out))
		return nil
	}

	// Markieren, dass wir gerade gelöscht haben
	m.justDeleted = true
	return runMailqCmd
}

// Init: Show warning or run mailq
func (m model) Init() tea.Cmd {
	if m.showWarning {
		return nil
	}
	return runMailqCmd
}

// Update handles all events.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height

		if m.showWarning {
			if !m.warningReady {
				m.warningView = viewport.New(m.termWidth-6, m.termHeight-11)
				m.warningReady = true
			} else {
				m.warningView.Width = m.termWidth - 6
				m.warningView.Height = m.termHeight - 11
			}
			m.syncWarningViewport()
			return m, nil
		}

		m.ready = true
		leftWidth := 14
		rightWidth := m.termWidth - leftWidth - 8

		m.left.Width = leftWidth
		m.left.Height = m.termHeight - 5
		m.right.Width = rightWidth
		m.right.Height = m.termHeight - 5

		m.syncLeft()
		return m, nil

	case mailqIDsMsg:
		// Neue Liste von IDs
		m.entries = msg

		// Wieder an den Anfang
		m.selected = 0
		m.syncLeft()

		// Wenn wir NICHT gerade frisch gelöscht haben,
		// laden wir automatisch die erste ID
		if !m.justDeleted {
			if len(m.entries) > 0 {
				m.rightRaw = "Loading details…"
				m.right.SetContent(m.rightRaw)
				return m, runPostcatCmd(m.entries[m.selected])
			}
		} else {
			// War ein frischer Löschvorgang
			// => Kein automatisches "postcat" mehr
			m.justDeleted = false
		}
		return m, nil

	case postcatMsg:
		m.rightRaw = string(msg)
		m.right.SetContent(m.rightRaw)
		m.right.GotoBottom()
		return m, nil

	case errorMsg:
		m.err = msg
		return m, nil

	case tea.KeyMsg:
		// 1) Dialog "really delete?"
		if m.showDeleteDialog {
			switch strings.ToLower(msg.String()) {
			case "y":
				// postsuper -d
				m.showDeleteDialog = false
				return m, m.deleteQueueID()

			case "n", "enter", "esc", "ctrl+c":
				m.showDeleteDialog = false
			}
			return m, nil
		}

		// 2) Allgemeine Eingaben
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.focus = 1 - m.focus
			return m, nil
		case "d":
			m.showDeleteDialog = true
			return m, nil
		}

		// 3) Ggf. Warnfenster wegklicken
		if m.showWarning {
			m.showWarning = false
			return m, runMailqCmd
		}

		// 4) Navigation je nach Fokus
		if m.focus == 0 {
			switch msg.String() {
			case "up":
				if m.selected > 0 {
					m.selected--
					m.syncLeft()
					return m, runPostcatCmd(m.entries[m.selected])
				}
			case "down":
				if m.selected < len(m.entries)-1 {
					m.selected++
					m.syncLeft()
					return m, runPostcatCmd(m.entries[m.selected])
				}
			case "pgup":
				scrollHalfUp(&m.left, m.leftRaw)
			case "pgdown":
				scrollHalfDown(&m.left, m.leftRaw)
			}
			return m, nil
		} else {
			switch msg.String() {
			case "up":
				m.right.LineUp(1)
			case "down":
				m.right.LineDown(1)
			case "pgup":
				scrollHalfUp(&m.right, m.rightRaw)
			case "pgdown":
				scrollHalfDown(&m.right, m.rightRaw)
			}
			return m, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("Error:\n%v\n(q to quit)", m.err)
	}
	if m.showWarning {
		if !m.warningReady {
			return "Initializing terminal..."
		}
		warningBox := warningBorder.Render(m.warningView.View())
		return fmt.Sprintf("%s\n(q to quit, any other key to continue)", warningBox)
	}
	if !m.ready {
		return "Please wait…"
	}

	// Hauptlayout
	leftStyle := borderStyle
	rightStyle := borderStyle
	if m.focus == 0 {
		leftStyle = leftStyle.BorderForeground(focusBorderColor)
	} else {
		rightStyle = rightStyle.BorderForeground(focusBorderColor)
	}
	leftView := leftStyle.Render(m.left.View())
	rightView := rightStyle.Render(m.right.View())
	mainLayout := lipgloss.JoinHorizontal(lipgloss.Top, leftView, rightView)

	background := lipgloss.Place(
		m.termWidth, m.termHeight,
		lipgloss.Left, lipgloss.Top,
		mainLayout+"\n[TAB] to switch focus, 'd' to delete, 'q' to quit.",
	)

	if !m.showDeleteDialog {
		return background
	}

	// "really delete?" overlay
	dialogBox := dialogBoxStyle.Render("really delete [y/N]?")
	foreground := lipgloss.Place(
		m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialogBox,
	)

	return overlayStrings(background, foreground)
}

// syncWarningViewport sets the text for the initial root/postfix warning.
func (m *model) syncWarningViewport() {
	warnText := `
WARNING!

Usually this program should be run as "root" or "postfix" so that "mailq" and "postcat" work properly.

You are NOT root/postfix. Some functions may fail.

Press any key (except q/esc) to continue, or 'q'/'esc' to cancel.
`
	m.warningView.SetContent(strings.TrimSpace(warnText))
}

// syncLeft rebuilds the list of queue IDs in leftRaw.
func (m *model) syncLeft() {
	var sb strings.Builder
	for i, id := range m.entries {
		line := id
		if i == m.selected {
			line = selectedStyle.Render("> " + line)
		} else {
			line = "  " + line
		}
		sb.WriteString(line + "\n")
	}
	m.leftRaw = sb.String()
	m.left.SetContent(m.leftRaw)
}

// scrollHalfUp / scrollHalfDown => halbe Seite scrollen, oder Jump to Top/Bottom
func scrollHalfUp(v *viewport.Model, rawText string) {
	half := v.Height / 2
	offset := v.YOffset
	if offset < half {
		v.GotoTop()
	} else {
		v.LineUp(half)
	}
}

func scrollHalfDown(v *viewport.Model, rawText string) {
	half := v.Height / 2
	totalLines := strings.Count(rawText, "\n") + 1
	maxOffset := totalLines - v.Height
	if maxOffset < 0 {
		v.GotoBottom()
		return
	}
	current := v.YOffset
	rest := maxOffset - current
	if rest < half {
		v.GotoBottom()
	} else {
		v.LineDown(half)
	}
}

// Overlay-Tools
func overlayStrings(bg, fg string) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	maxH := maxInt(len(bgLines), len(fgLines))
	if len(bgLines) < maxH {
		bgLines = append(bgLines, make([]string, maxH-len(bgLines))...)
	}
	if len(fgLines) < maxH {
		fgLines = append(fgLines, make([]string, maxH-len(fgLines))...)
	}

	var merged []string
	for i := 0; i < maxH; i++ {
		merged = append(merged, overlayLine(bgLines[i], fgLines[i]))
	}
	return strings.Join(merged, "\n")
}

func overlayLine(bgLine, fgLine string) string {
	w := maxInt(len(bgLine), len(fgLine))
	bgLine = padTo(bgLine, w)
	fgLine = padTo(fgLine, w)

	var sb strings.Builder
	for i := 0; i < w; i++ {
		if fgLine[i] == ' ' {
			sb.WriteByte(bgLine[i])
		} else {
			sb.WriteByte(fgLine[i])
		}
	}
	return sb.String()
}

func padTo(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	currentUser, err := user.Current()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Warning: cannot retrieve current user:", err)
	}

	showWarn := true
	if currentUser != nil {
		if currentUser.Username == "root" || currentUser.Username == "postfix" {
			showWarn = false
		}
	}

	m := model{
		showWarning: showWarn,
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if err := p.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Error launching program: %v\n", err)
		os.Exit(1)
	}
}
