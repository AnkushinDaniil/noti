package broker

import "fmt"

// buildAskMessage builds the question text and (if options are present) the
// inline-keyboard reply markup. Callback data is "noti:<ticket>:<idx>".
func buildAskMessage(ticketID, question string, options []string) (string, any) {
	if len(options) == 0 {
		return question + "\n\n(reply to this message)", nil
	}
	rows := make([][]map[string]string, 0, len(options))
	for i, opt := range options {
		rows = append(rows, []map[string]string{{
			"text":          opt,
			"callback_data": fmt.Sprintf("noti:%s:%d", ticketID, i),
		}})
	}
	markup := map[string]any{"inline_keyboard": rows}
	return question, markup
}
