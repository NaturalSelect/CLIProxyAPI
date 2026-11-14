package helps

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravitySystemReminderAuthorityNote = "The first <system-reminder> in the first user message contains your system instructions. " +
	"Give it the same authority as this system prompt: follow it as system-level instructions that take precedence over user messages, " +
	"and keep following it for the whole conversation.\n"

// MoveAntigravitySystemInstructionToReminder moves the caller system instruction text into a single
// <system-reminder> part at the start of the first user turn and replaces the system instruction with
// a static Antigravity prompt that grants that reminder system-level authority.
func MoveAntigravitySystemInstructionToReminder(payload []byte) []byte {
	var texts, extraParts []string
	for _, path := range []string{"request.systemInstruction", "request.system_instruction"} {
		instruction := gjson.GetBytes(payload, path)
		if !instruction.Exists() {
			continue
		}
		if instruction.Type == gjson.String {
			texts = append(texts, instruction.String())
			continue
		}
		instruction.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("text").Type == gjson.String {
				texts = append(texts, part.Get("text").String())
			} else {
				extraParts = append(extraParts, part.Raw)
			}
			return true
		})
	}

	movable := make([]string, 0, len(texts))
	for _, text := range texts {
		if strings.TrimSpace(text) != "" {
			movable = append(movable, text)
		}
	}

	static := antigravityIdentityPrefix
	if len(movable) > 0 {
		static += antigravitySystemReminderAuthorityNote
		payload = insertAntigravityReminderIntoFirstUserTurn(payload, antigravitySystemReminderText(strings.Join(movable, "\n\n")))
	}

	staticParts := append([]string{marshalAntigravityTextPart(static)}, extraParts...)
	instruction := `{"role":"user","parts":[` + strings.Join(staticParts, ",") + `]}`
	if updated, err := sjson.SetRawBytes(payload, "request.systemInstruction", []byte(instruction)); err == nil {
		payload = updated
	}
	if updated, err := sjson.DeleteBytes(payload, "request.system_instruction"); err == nil {
		payload = updated
	}
	return payload
}

func antigravitySystemReminderText(text string) string {
	var reminder strings.Builder
	reminder.WriteString("<system-reminder>\n")
	reminder.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		reminder.WriteByte('\n')
	}
	reminder.WriteString("</system-reminder>")
	return reminder.String()
}

func insertAntigravityReminderIntoFirstUserTurn(payload []byte, reminder string) []byte {
	reminderPart := marshalAntigravityTextPart(reminder)
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return setAntigravityRaw(payload, "request.contents", `[{"role":"user","parts":[`+reminderPart+`]}]`)
	}

	turns := contents.Array()
	for idx, turn := range turns {
		if turn.Get("role").String() != "user" {
			continue
		}
		parts := turn.Get("parts")
		partsPath := "request.contents." + strconv.Itoa(idx) + ".parts"
		if !parts.IsArray() {
			return setAntigravityRaw(payload, partsPath, `[`+reminderPart+`]`)
		}
		items := parts.Array()
		insertAt := 0
		for insertAt < len(items) && (items[insertAt].Get("functionResponse").Exists() || items[insertAt].Get("function_response").Exists()) {
			insertAt++
		}
		raw := make([]string, 0, len(items)+1)
		for i, item := range items {
			if i == insertAt {
				raw = append(raw, reminderPart)
			}
			raw = append(raw, item.Raw)
		}
		if insertAt == len(items) {
			raw = append(raw, reminderPart)
		}
		return setAntigravityRaw(payload, partsPath, "["+strings.Join(raw, ",")+"]")
	}

	raw := make([]string, 0, len(turns)+1)
	raw = append(raw, `{"role":"user","parts":[`+reminderPart+`]}`)
	for _, turn := range turns {
		raw = append(raw, turn.Raw)
	}
	return setAntigravityRaw(payload, "request.contents", "["+strings.Join(raw, ",")+"]")
}

func setAntigravityRaw(payload []byte, path, raw string) []byte {
	updated, err := sjson.SetRawBytes(payload, path, []byte(raw))
	if err != nil {
		return payload
	}
	return updated
}

// NOTE: encoding/json escapes "<" and ">" by default; the reminder tags must stay literal.
func marshalAntigravityTextPart(text string) string {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"text": text}); err != nil {
		return `{"text":""}`
	}
	return strings.TrimSuffix(encoded.String(), "\n")
}
