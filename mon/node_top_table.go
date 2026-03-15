package monitor

import (
	"strings"

	mview "github.com/blacknon/mview"
	"github.com/mattn/go-runewidth"
)

type topTableState struct {
	selectedRow    int
	selectedColumn int
	offsetRow      int
	offsetColumn   int
	selectedKey    string
}

func captureTopTableState(table *mview.Table, keyColumn int) topTableState {
	selectedRow, selectedColumn := table.GetSelection()
	offsetRow, offsetColumn := table.GetOffset()

	state := topTableState{
		selectedRow:    selectedRow,
		selectedColumn: selectedColumn,
		offsetRow:      offsetRow,
		offsetColumn:   offsetColumn,
	}

	if selectedRow > 0 {
		state.selectedKey = table.GetCell(selectedRow, keyColumn).GetText()
	}

	return state
}

func restoreTopTableState(table *mview.Table, state topTableState, keyColumn int) {
	rowCount := table.GetRowCount()
	if rowCount <= 0 {
		return
	}

	selectedRow := state.selectedRow
	if state.selectedKey != "" {
		if row := findTopTableRowByText(table, keyColumn, state.selectedKey); row >= 0 {
			selectedRow = row
		}
	}

	if selectedRow >= rowCount {
		selectedRow = rowCount - 1
	}
	if selectedRow < 0 {
		selectedRow = 0
	}

	selectedColumn := state.selectedColumn
	if selectedColumn < 0 {
		selectedColumn = 0
	}

	table.Select(selectedRow, selectedColumn)

	offsetRow := state.offsetRow
	maxOffsetRow := rowCount - 1
	if offsetRow > maxOffsetRow {
		offsetRow = maxOffsetRow
	}
	if offsetRow < 0 {
		offsetRow = 0
	}

	offsetColumn := state.offsetColumn
	if offsetColumn < 0 {
		offsetColumn = 0
	}

	table.SetOffset(offsetRow, offsetColumn)
}

func findTopTableRowByText(table *mview.Table, column int, text string) int {
	for row := 1; row < table.GetRowCount(); row++ {
		if table.GetCell(row, column).GetText() == text {
			return row
		}
	}

	return -1
}

func trimTopTableRows(table *mview.Table, rowCount int) {
	for table.GetRowCount() > rowCount {
		table.RemoveRow(table.GetRowCount() - 1)
	}
}

func padTableText(text string, minWidth int) string {
	if minWidth <= 0 {
		return text
	}

	width := runewidth.StringWidth(text)
	if width >= minWidth {
		return text
	}

	return text + strings.Repeat(" ", minWidth-width)
}
